package ent

// generate.go configures generation of typed Ent queries and mutations from
// the persistence schema. Generation enables upserts, interceptors, raw SQL
// execution, and row locking so handwritten repositories can use those
// operations through the generated client and transaction types.

// sql/execquery generates ExecContext and QueryContext forwarding methods for raw
// SQL in transactions.
// sql/lock enables FOR UPDATE row locks.
//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/upsert,intercept,sql/execquery,sql/lock --idtype int64 ./schema
