package model

import "github.com/caos/zitadel/internal/eventstore/models"

const (
	KeyPairAggregate models.AggregateType = "key_pair"

	KeyPairAdded models.EventType = "key_pair.added"
)
