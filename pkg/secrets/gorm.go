package secrets

import (
	"context"
	"fmt"
	"reflect"

	"gorm.io/gorm/schema"
)

// SealedColumnSerializer backs string columns that store already-SEALED secret
// values (tagged `gorm:"serializer:sealed"`).
//
// It deliberately does NOT decrypt on read: a loaded row holds the ciphertext
// (an "enc:u1:…"/"enc:v1:…" string), never the plaintext. Callers decrypt
// explicitly, at the point of use, via the model's Decrypt* accessors
// (secrets.Open under the hood) — so a raw secret exists in memory only for the
// instant it is actually needed, and never lingers on a long-lived struct.
//
// On write it refuses a non-empty value that is not already sealed, so a
// forgotten SealForUser call fails loudly instead of silently persisting a
// secret in the clear.
type SealedColumnSerializer struct{}

var _ schema.SerializerInterface = SealedColumnSerializer{}

func (SealedColumnSerializer) Scan(ctx context.Context, field *schema.Field, dst reflect.Value, dbValue interface{}) error {
	var stored string
	switch v := dbValue.(type) {
	case nil:
	case string:
		stored = v
	case []byte:
		stored = string(v)
	default:
		return fmt.Errorf("secrets: field %s has unsupported db type %T", field.Name, dbValue)
	}
	// Pass the ciphertext through unchanged — no decryption on load.
	field.ReflectValueOf(ctx, dst).SetString(stored)
	return nil
}

func (SealedColumnSerializer) Value(ctx context.Context, field *schema.Field, dst reflect.Value, fieldValue interface{}) (interface{}, error) {
	s, ok := fieldValue.(string)
	if !ok {
		return nil, fmt.Errorf("secrets: field %s must be a string, got %T", field.Name, fieldValue)
	}
	if s != "" && !IsSealed(s) {
		return nil, fmt.Errorf("secrets: refusing to store an unsealed value in %s — seal it with SealForUser first", field.Name)
	}
	return s, nil
}
