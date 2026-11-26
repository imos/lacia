package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/imos/lacia/internal/routine"
)

func runRoutine(args []string) error {
	if len(args) != 4 || args[0] != "report" {
		return errors.New("使い方: lacia run report <定期タスクID> <実行ID> <JSONファイルまたは->")
	}
	if apiBase() == "" {
		return errors.New(apiAddressHelp)
	}
	var f *os.File
	if args[3] == "-" {
		f = os.Stdin
	} else {
		var err error
		f, err = os.Open(args[3])
		if err != nil {
			return err
		}
		defer f.Close()
	}
	var report routine.Result
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("定期タスクの結果のJSONを読めませんでした")
	}
	return apiCall(http.MethodPost, "/api/routines/"+url.PathEscape(args[1])+"/runs/"+url.PathEscape(args[2]), report, nil)
}
