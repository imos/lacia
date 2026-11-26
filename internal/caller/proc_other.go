//go:build !darwin && !linux

package caller

import "errors"

func lookup(int) (process, error) {
	return process{}, errors.New("このシステムでは、実行中の会話を調べられません")
}
