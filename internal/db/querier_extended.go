package db

import "database/sql"

// QuerierWithTx extends the Querier interface with transaction support
type QuerierWithTx interface {
	Querier
	WithTx(tx *sql.Tx) QuerierWithTx
}

// Ensure Queries implements QuerierWithTx
var _ QuerierWithTx = (*queriesWrapper)(nil)
var _ QuerierWithTx = (*mysqlQuerierWrapper)(nil)

// queriesWrapper wraps Queries to implement QuerierWithTx
type queriesWrapper struct {
	*Queries
}

func (q *queriesWrapper) WithTx(tx *sql.Tx) QuerierWithTx {
	return &queriesWrapper{Queries: q.Queries.WithTx(tx)}
}

// mysqlQuerierWrapper wraps MySQLQuerier to implement QuerierWithTx
type mysqlQuerierWrapper struct {
	*MySQLQuerier
}

func (q *mysqlQuerierWrapper) WithTx(tx *sql.Tx) QuerierWithTx {
	return &mysqlQuerierWrapper{MySQLQuerier: q.MySQLQuerier.WithTx(tx)}
}
