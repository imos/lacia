package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/imos/lacia/internal/daylog"
)

// MaxAttachmentBytes is a conservative application limit, independent of
// Discord's account-specific upload allowance.
const MaxAttachmentBytes = 8 << 20

var pdfName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.pdf$`)

// AttachmentResult is independent of the request's text delivery status.
// Unknown includes interrupted sends and is never retried automatically.
type AttachmentResult struct {
	RequestID string `json:"request_id"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	MessageID string `json:"message_id,omitempty"`
}

// ReadPDF opens only a regular PDF below root/local, including during symlink races.
// Paths are relative to the dedicated export directory, never arbitrary paths.
func ReadPDF(root, path string) (string, []byte, error) {
	if !filepath.IsLocal(path) || !pdfName.MatchString(filepath.Base(path)) {
		return "", nil, errors.New("local/ からの相対パスを指定してください。ファイル名の先頭は半角英数字、末尾は .pdf にしてください。使える文字は半角英数字とピリオド、アンダースコア、ハイフンです")
	}
	repo, err := os.OpenRoot(root)
	if err != nil {
		return "", nil, errors.New("添付ファイルを探す local ディレクトリーを開けませんでした")
	}
	defer repo.Close()
	dir, err := repo.OpenRoot("local")
	if err != nil {
		return "", nil, errors.New("添付ファイルを探す local ディレクトリーを開けませんでした")
	}
	defer dir.Close()
	f, err := dir.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", nil, errors.New("local 内の添付ファイルを開けませんでした")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxAttachmentBytes {
		return "", nil, errors.New("添付できるのは8 MiB以下の通常のPDFファイルです")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxAttachmentBytes+1))
	if err != nil || len(data) > MaxAttachmentBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return "", nil, errors.New("ファイルをPDFとして読めませんでした。PDF形式で、サイズが8 MiB以下かを確かめてください")
	}
	return filepath.Base(path), data, nil
}

// SendPDF records a durable claim before making a single HTTP attempt. The
// claim is keyed by destination and bytes, not by caller-chosen request IDs.
// A crash, timeout, malformed response or rejected request keeps it unknown;
// repeating this call only returns the saved outcome, including after restart.
func (b *Bot) SendPDF(ctx context.Context, channel, requestID, filename string, data []byte) (AttachmentResult, error) {
	result := AttachmentResult{RequestID: requestID, Filename: filename, Status: "unknown"}
	if channel == "" || requestID == "" || !pdfName.MatchString(filename) || len(data) > MaxAttachmentBytes || !bytes.HasPrefix(data, []byte("%PDF-")) {
		return result, errors.New("PDFを送れません。送り先と元の依頼IDに加え、ファイル名、形式、サイズ（8 MiB以下）を確かめてください")
	}
	digest := sha256.Sum256(append(append([]byte(channel), 0), data...))
	key := hex.EncodeToString(digest[:])
	b.attachmentsMu.Lock()
	defer b.attachmentsMu.Unlock()
	dir := b.cfg.State + ".attachments"
	if b.cfg.State == "" {
		return result, errors.New("添付の送信記録の保存先がありません")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return result, err
	}
	path := filepath.Join(dir, key+".json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		saved, err := os.ReadFile(path)
		if err != nil {
			return result, err
		}
		err = json.Unmarshal(saved, &result)
		return result, err
	}
	if err != nil {
		return result, err
	}
	payload, _ := json.Marshal(result)
	_, err = f.Write(payload)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = syncAttachmentDir(dir)
	}
	// Also persist creation of the ledger directory itself.
	if err == nil {
		err = syncAttachmentDir(filepath.Dir(dir))
	}
	if err != nil {
		return result, err
	}
	messageID, err := b.rest.postPDF(ctx, channel, filename, data, key[:24])
	if err != nil {
		return result, err
	}
	result.Status, result.MessageID = "sent", messageID
	b.record(daylog.Entry{Key: "file:" + messageID, At: time.Now(), Who: "レイシア", Place: b.place(ctx, channel),
		Files: []daylog.File{{Name: filename, Type: "application/pdf", Size: int64(len(data))}}})
	payload, _ = json.Marshal(result)
	tmp, err := os.CreateTemp(dir, ".result-")
	if err != nil {
		return result, err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(payload)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr = tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err == nil {
		err = syncAttachmentDir(dir)
	}
	return result, err
}

func syncAttachmentDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// postPDF intentionally does not use do: uploads must never be replayed,
// including after a rate limit or an unreadable response.
func (r *rest) postPDF(ctx context.Context, channel, filename string, data []byte, nonce string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	payload, err := writer.CreateFormField("payload_json")
	if err != nil {
		return "", err
	}
	err = json.NewEncoder(payload).Encode(map[string]any{
		"nonce": nonce, "enforce_nonce": true,
		"allowed_mentions": map[string]any{"parse": []string{}, "replied_user": false},
		"attachments":      []map[string]any{{"id": 0, "filename": filename}},
	})
	if err != nil {
		return "", err
	}
	file, err := writer.CreateFormFile("files[0]", filename)
	if err != nil {
		return "", err
	}
	if _, err = file.Write(data); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", r.api+"/channels/"+channel+"/messages", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+r.token)
	req.Header.Set("User-Agent", "DiscordBot (https://github.com/imos/lacia, 1)")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	// Redirects must not replay a POST or forward its credentials elsewhere.
	client := *r.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("PDFがDiscordに届いたか、確かめられませんでした。自動では再送しません")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PDFを送ったときのDiscordの応答は HTTP %d でした。自動では再送しません", resp.StatusCode)
	}
	var out struct {
		ID          string `json:"id"`
		ChannelID   string `json:"channel_id"`
		Attachments []struct {
			Filename string `json:"filename"`
			Size     int    `json:"size"`
			ID       string `json:"id"`
		} `json:"attachments"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", errors.New("PDFを送ったあとのDiscordの応答を読めませんでした。自動では再送しません")
	}
	if out.ID == "" || out.ChannelID != channel || len(out.Attachments) != 1 || out.Attachments[0].ID == "" || out.Attachments[0].Filename != filename || out.Attachments[0].Size != len(data) {
		return "", errors.New("Discordの応答でPDFの添付を確認できませんでした。自動では再送しません")
	}
	return out.ID, nil
}
