package backup

import "context"

// EnsureHelperImage makes the pinned helper image available locally, pulling
// it only when it is missing.
func EnsureHelperImage(ctx context.Context, eng Engine, out func(string)) error {
	if _, err := eng.InspectImage(ctx, HelperImage); err == nil {
		return nil
	}
	return eng.PullImage(ctx, HelperImage, out)
}
