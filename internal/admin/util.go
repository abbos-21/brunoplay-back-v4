package admin

import (
	"strconv"

	"github.com/jackc/pgx/v5"
)

type pgxTx = pgx.Tx

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

func parseID(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
