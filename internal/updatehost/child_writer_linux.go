package updatehost

import (
	"context"
	"github.com/rcss-labs/stackstead/internal/update"
	"os"
)

func writeChildPrefix(context.Context, update.Instance, int64, *os.File, *os.File) error {
	return update.ErrUnavailable
}
