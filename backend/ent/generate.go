package ent

// sql/execquery generates ExecContext and QueryContext forwarding methods for raw SQL in transactions.
// sql/lock enables FOR UPDATE row locks.
//go:generate go run -mod=mod entgo.io/ent/cmd/ent generate --feature sql/upsert,intercept,sql/execquery,sql/lock --idtype int64 ./schema
