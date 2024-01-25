package command

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"strconv"
	"time"

	"github.com/zitadel/zitadel/internal/api/authz"
	api_http "github.com/zitadel/zitadel/internal/api/http"
	"github.com/zitadel/zitadel/internal/command/preparation"
	sd "github.com/zitadel/zitadel/internal/config/systemdefaults"
	"github.com/zitadel/zitadel/internal/crypto"
	"github.com/zitadel/zitadel/internal/domain"
	"github.com/zitadel/zitadel/internal/errors"
	"github.com/zitadel/zitadel/internal/eventstore"
	"github.com/zitadel/zitadel/internal/id"
	"github.com/zitadel/zitadel/internal/static"
	webauthn_helper "github.com/zitadel/zitadel/internal/webauthn"
)

type Commands struct {
	httpClient *http.Client

	checkPermission    domain.PermissionCheck
	newCode            cryptoCodeFunc
	newCodeWithDefault cryptoCodeWithDefaultFunc

	eventstore     *eventstore.Eventstore
	static         static.Storage
	idGenerator    id.Generator
	zitadelRoles   []authz.RoleMapping
	externalDomain string
	externalSecure bool
	externalPort   uint16

	idpConfigEncryption             crypto.EncryptionAlgorithm
	smtpEncryption                  crypto.EncryptionAlgorithm
	smsEncryption                   crypto.EncryptionAlgorithm
	userEncryption                  crypto.EncryptionAlgorithm
	userPasswordHasher              *crypto.PasswordHasher
	codeAlg                         crypto.HashAlgorithm
	machineKeySize                  int
	applicationKeySize              int
	domainVerificationAlg           crypto.EncryptionAlgorithm
	domainVerificationGenerator     crypto.Generator
	domainVerificationValidator     func(domain, token, verifier string, checkType api_http.CheckType) error
	sessionTokenCreator             func(sessionID string) (id string, token string, err error)
	sessionTokenVerifier            func(ctx context.Context, sessionToken, sessionID, tokenID string) (err error)
	defaultAccessTokenLifetime      time.Duration
	defaultRefreshTokenLifetime     time.Duration
	defaultRefreshTokenIdleLifetime time.Duration

	multifactors            domain.MultifactorConfigs
	webauthnConfig          *webauthn_helper.Config
	keySize                 int
	keyAlgorithm            crypto.EncryptionAlgorithm
	certificateAlgorithm    crypto.EncryptionAlgorithm
	certKeySize             int
	privateKeyLifetime      time.Duration
	publicKeyLifetime       time.Duration
	certificateLifetime     time.Duration
	defaultSecretGenerators *SecretGenerators

	samlCertificateAndKeyGenerator func(id string) ([]byte, []byte, error)
}

func StartCommands(
	es *eventstore.Eventstore,
	defaults sd.SystemDefaults,
	zitadelRoles []authz.RoleMapping,
	staticStore static.Storage,
	webAuthN *webauthn_helper.Config,
	externalDomain string,
	externalSecure bool,
	externalPort uint16,
	idpConfigEncryption, otpEncryption, smtpEncryption, smsEncryption, userEncryption, domainVerificationEncryption, oidcEncryption, samlEncryption crypto.EncryptionAlgorithm,
	httpClient *http.Client,
	permissionCheck domain.PermissionCheck,
	sessionTokenVerifier func(ctx context.Context, sessionToken string, sessionID string, tokenID string) (err error),
	defaultAccessTokenLifetime,
	defaultRefreshTokenLifetime,
	defaultRefreshTokenIdleLifetime time.Duration,
	defaultSecretGenerators *SecretGenerators,
) (repo *Commands, err error) {
	if externalDomain == "" {
		return nil, errors.ThrowInvalidArgument(nil, "COMMAND-Df21s", "no external domain specified")
	}
	idGenerator := id.SonyFlakeGenerator()
	// reuse the oidcEncryption to be able to handle both tokens in the interceptor later on
	sessionAlg := oidcEncryption
	repo = &Commands{
		eventstore:                      es,
		static:                          staticStore,
		idGenerator:                     idGenerator,
		zitadelRoles:                    zitadelRoles,
		externalDomain:                  externalDomain,
		externalSecure:                  externalSecure,
		externalPort:                    externalPort,
		keySize:                         defaults.KeyConfig.Size,
		certKeySize:                     defaults.KeyConfig.CertificateSize,
		privateKeyLifetime:              defaults.KeyConfig.PrivateKeyLifetime,
		publicKeyLifetime:               defaults.KeyConfig.PublicKeyLifetime,
		certificateLifetime:             defaults.KeyConfig.CertificateLifetime,
		idpConfigEncryption:             idpConfigEncryption,
		smtpEncryption:                  smtpEncryption,
		smsEncryption:                   smsEncryption,
		userEncryption:                  userEncryption,
		domainVerificationAlg:           domainVerificationEncryption,
		keyAlgorithm:                    oidcEncryption,
		certificateAlgorithm:            samlEncryption,
		webauthnConfig:                  webAuthN,
		httpClient:                      httpClient,
		checkPermission:                 permissionCheck,
		newCode:                         newCryptoCode,
		newCodeWithDefault:              newCryptoCodeWithDefaultConfig,
		sessionTokenCreator:             sessionTokenCreator(idGenerator, sessionAlg),
		sessionTokenVerifier:            sessionTokenVerifier,
		defaultAccessTokenLifetime:      defaultAccessTokenLifetime,
		defaultRefreshTokenLifetime:     defaultRefreshTokenLifetime,
		defaultRefreshTokenIdleLifetime: defaultRefreshTokenIdleLifetime,
		defaultSecretGenerators:         defaultSecretGenerators,
		samlCertificateAndKeyGenerator:  samlCertificateAndKeyGenerator(defaults.KeyConfig.Size),
	}

	repo.codeAlg = crypto.NewBCrypt(defaults.SecretGenerators.PasswordSaltCost)
	repo.userPasswordHasher, err = defaults.PasswordHasher.PasswordHasher()
	if err != nil {
		return nil, err
	}
	repo.machineKeySize = int(defaults.SecretGenerators.MachineKeySize)
	repo.applicationKeySize = int(defaults.SecretGenerators.ApplicationKeySize)

	repo.multifactors = domain.MultifactorConfigs{
		OTP: domain.OTPConfig{
			CryptoMFA: otpEncryption,
			Issuer:    defaults.Multifactors.OTP.Issuer,
		},
	}

	repo.domainVerificationGenerator = crypto.NewEncryptionGenerator(defaults.DomainVerification.VerificationGenerator, repo.domainVerificationAlg)
	repo.domainVerificationValidator = api_http.ValidateDomain
	return repo, nil
}

type AppendReducer interface {
	AppendEvents(...eventstore.Event)
	// TODO: Why is it allowed to return an error here?
	Reduce() error
}

func (c *Commands) pushAppendAndReduce(ctx context.Context, object AppendReducer, cmds ...eventstore.Command) error {
	events, err := c.eventstore.Push(ctx, cmds...)
	if err != nil {
		return err
	}
	return AppendAndReduce(object, events...)
}

func AppendAndReduce(object AppendReducer, events ...eventstore.Event) error {
	object.AppendEvents(events...)
	return object.Reduce()
}

func queryAndReduce(ctx context.Context, filter preparation.FilterToQueryReducer, wm eventstore.QueryReducer) error {
	events, err := filter(ctx, wm.Query())
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	wm.AppendEvents(events...)
	return wm.Reduce()
}

type existsWriteModel interface {
	Exists() bool
	eventstore.QueryReducer
}

func exists(ctx context.Context, filter preparation.FilterToQueryReducer, wm existsWriteModel) (bool, error) {
	err := queryAndReduce(ctx, filter, wm)
	if err != nil {
		return false, err
	}
	return wm.Exists(), nil
}

func samlCertificateAndKeyGenerator(keySize int) func(id string) ([]byte, []byte, error) {
	return func(id string) ([]byte, []byte, error) {
		priv, pub, err := crypto.GenerateKeyPair(keySize)
		if err != nil {
			return nil, nil, err
		}

		serial, err := strconv.Atoi(id)
		if err != nil {
			return nil, nil, err
		}
		template := x509.Certificate{
			SerialNumber: big.NewInt(int64(serial)),
			Subject: pkix.Name{
				Organization: []string{"ZITADEL"},
				SerialNumber: id,
			},
			KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
			ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			BasicConstraintsValid: true,
		}

		derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, pub, priv)
		if err != nil {
			return nil, nil, errors.ThrowInternalf(err, "COMMAND-x92u101j", "failed to create certificate")
		}

		keyBlock := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)}
		certBlock := &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}
		return pem.EncodeToMemory(keyBlock), pem.EncodeToMemory(certBlock), nil
	}
}
