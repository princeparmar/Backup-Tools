package utils

import (
	"encoding/json"

	"github.com/StorX2-0/Backup-Tools/pkg/database"
)

func JsonMap[K comparable, V any](s string) (map[K]V, error) {
	if s == "" {
		return map[K]V{}, nil
	}

	data := map[K]V{}
	err := json.Unmarshal([]byte(s), &data)

	return data, err
}

type OrderedMap[K comparable, V any] struct {
	pairs map[K]V
	list  *ComparableList[K]
}

func NewOrderedMap[K comparable, V any]() *OrderedMap[K, V] {
	return &OrderedMap[K, V]{
		pairs: map[K]V{},
		list:  NewComparableList[K](),
	}
}

func (om *OrderedMap[K, V]) Add(key K, value V) {
	om.pairs[key] = value
	om.list.Add(key)
}

func (om *OrderedMap[K, V]) Remove(key K) {
	delete(om.pairs, key)
	om.list.Remove(key)
}

func (om *OrderedMap[K, V]) Get(key K) (V, bool) {
	value, ok := om.pairs[key]
	return value, ok
}

func (om *OrderedMap[K, V]) Size() int {
	return len(om.pairs)
}

func (om *OrderedMap[K, V]) ToSlice() []V {
	slice := make([]V, 0, len(om.pairs))
	for _, key := range om.list.items {
		slice = append(slice, om.pairs[key])
	}
	return slice
}

// ToDbSlice converts a slice to a database.DbSlice
func ToDbSlice[T any](slice []T) *database.DbSlice[T] {
	return database.NewDbSliceFromSlice(slice)
}
