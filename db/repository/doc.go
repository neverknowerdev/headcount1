// Package repository contains table-oriented persistence. Each repository is
// defined in one file and owns the queries and mutations whose primary record
// is that repository's table. Cross-table validation and transactional writes
// remain explicit inside the owning operation.
package repository
