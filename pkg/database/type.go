package database

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
)

type DbJson[V any] struct {
	v *V
}

func NewDbJson[V any]() *DbJson[V] {
	return &DbJson[V]{}
}

func NewDbJsonFromValue[V any](v V) *DbJson[V] {
	return &DbJson[V]{
		v: &v,
	}
}

func (d *DbJson[V]) Scan(value any) error {

	switch value := value.(type) {
	case nil:
		return nil
	case []uint8:
		if err := json.Unmarshal([]byte(value), &d.v); err != nil {
			// If unmarshaling fails, initialize with zero value
			var zero V
			d.v = &zero
			return nil
		}
		return nil
	case string:
		if err := json.Unmarshal([]byte(value), &d.v); err != nil {
			// If unmarshaling fails, initialize with zero value
			var zero V
			d.v = &zero
			return nil
		}
		return nil
	}

	return nil
}

func (d *DbJson[V]) Value() (driver.Value, error) {
	v, err := json.Marshal(d.v)
	return string(v), err
}

func (d *DbJson[V]) Json() *V {
	return d.v
}

// String implements fmt.Stringer interface for proper JSON serialization
func (d *DbJson[V]) String() string {
	if d.v == nil {
		return "{}"
	}
	data, err := json.Marshal(d.v)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// MarshalJSON implements json.Marshaler.
func (d *DbJson[V]) MarshalJSON() ([]byte, error) {
	if d.v == nil {
		return []byte("null"), nil
	}
	return json.Marshal(d.v)
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *DbJson[V]) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return nil
	}

	v := new(V)
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}

	d.v = v
	return nil
}

type DbMap[K comparable, V any] struct {
	m map[K]V
}

func NewDbMap[K comparable, V any]() *DbMap[K, V] {
	return &DbMap[K, V]{
		m: make(map[K]V),
	}
}

func NewDbMapFromMap[K comparable, V any](m map[K]V) *DbMap[K, V] {
	return &DbMap[K, V]{
		m: m,
	}
}

func (d *DbMap[K, V]) Scan(value any) error {

	switch value := value.(type) {
	case nil:
		return nil
	case []uint8:
		return json.Unmarshal([]byte(value), &d.m)
	case string:
		return json.Unmarshal([]byte(value), &d.m)
	}

	return nil
}

func (d *DbMap[K, V]) Value() (driver.Value, error) {
	if d == nil {
		return nil, nil
	}
	if d.m == nil {
		return "null", nil
	}
	v, err := json.Marshal(d.m)
	return string(v), err
}

func (d *DbMap[K, V]) Map() map[K]V {
	return d.m
}

// MarshalJSON implements json.Marshaler.
func (d *DbMap[K, V]) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.m)
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *DbMap[K, V]) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &d.m)
}

type DbSlice[V any] struct {
	s []V
}

func NewDbSlice[V any]() *DbSlice[V] {
	return &DbSlice[V]{
		s: make([]V, 0),
	}
}

func NewDbSliceFromSlice[V any](s []V) *DbSlice[V] {
	return &DbSlice[V]{
		s: s,
	}
}

func (d *DbSlice[V]) Scan(value any) error {
	switch value := value.(type) {
	case nil:
		return nil
	case []uint8:
		return json.Unmarshal([]byte(value), &d.s)
	case string:
		return json.Unmarshal([]byte(value), &d.s)
	}
	return nil
}

func (d *DbSlice[V]) Value() (driver.Value, error) {
	if d == nil {
		return nil, nil
	}

	v, err := json.Marshal(d.s)
	return string(v), err
}

func (d *DbSlice[V]) Slice() []V {
	return d.s
}

// MarshalJSON implements json.Marshaler.
func (d *DbSlice[V]) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.s)
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *DbSlice[V]) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &d.s)
}

func NullIntToPtr(n sql.NullInt64) *int {
	if !n.Valid {
		return nil
	}
	val := int(n.Int64)
	return &val
}
