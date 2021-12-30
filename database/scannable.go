package database

type Scannable interface {
	Scan(...interface{}) error
}
