package gorm

type Database interface {
	Close() error
	Migrate(models ...interface{}) error
	HealthCheck() error
	Transaction(fn func(tx Database) error) error
}

type Repository[T any] interface {
	Create(entity *T) error
	GetByID(id uint) (*T, error)
	Update(entity *T) error
	Delete(id uint) error
	FindAll(conditions ...interface{}) ([]T, error)
	Count(conditions ...interface{}) (int64, error)
}
