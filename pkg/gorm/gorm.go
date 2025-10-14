package gorm

import "gorm.io/gorm"

type GormModel struct {
	gorm.Model
}

type DB struct{ *gorm.DB }

func NewDB(gormDB *gorm.DB) *DB { return &DB{DB: gormDB} }

func (db *DB) GetGormDB() *gorm.DB { return db.DB }

func (db *DB) Close() error {
	sqlDB, err := db.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

func (db *DB) Migrate(models ...interface{}) error {
	return db.DB.AutoMigrate(models...)
}

func (db *DB) HealthCheck() error {
	sqlDB, err := db.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Ping()
}

func (db *DB) Transaction(fn func(tx *DB) error) error {
	return db.DB.Transaction(func(tx *gorm.DB) error {
		return fn(&DB{DB: tx})
	})
}

// Enhanced Generic Repository with more methods
type GenericRepository[T any] struct{ db *DB }

func NewGenericRepository[T any](db *DB) *GenericRepository[T] {
	return &GenericRepository[T]{db: db}
}

func (r *GenericRepository[T]) Create(entity *T) error {
	return r.db.Create(entity).Error
}

func (r *GenericRepository[T]) GetByID(id uint) (*T, error) {
	var entity T
	err := r.db.First(&entity, id).Error
	return &entity, err
}

func (r *GenericRepository[T]) Update(entity *T) error {
	return r.db.Save(entity).Error
}

func (r *GenericRepository[T]) Delete(id uint) error {
	var entity T
	return r.db.Delete(&entity, id).Error
}

func (r *GenericRepository[T]) FindAll(conditions ...interface{}) ([]T, error) {
	var entities []T
	query := r.db.DB
	for _, cond := range conditions {
		query = query.Where(cond)
	}
	err := query.Find(&entities).Error
	return entities, err
}

func (r *GenericRepository[T]) Count(conditions ...interface{}) (int64, error) {
	var count int64
	var entity T
	query := r.db.Model(&entity)
	for _, cond := range conditions {
		query = query.Where(cond)
	}
	return count, query.Count(&count).Error
}

// New methods
func (r *GenericRepository[T]) First(conditions ...interface{}) (*T, error) {
	var entity T
	query := r.db.DB
	for _, cond := range conditions {
		query = query.Where(cond)
	}
	err := query.First(&entity).Error
	return &entity, err
}

func (r *GenericRepository[T]) Exists(conditions ...interface{}) (bool, error) {
	count, err := r.Count(conditions...)
	return count > 0, err
}
