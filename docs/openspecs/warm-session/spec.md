# Spec: Warm session (khởi động CLI trước, gửi một turn sau)

## Goal
Cho phép ứng dụng khởi động một tiến trình CLI trước khi có prompt, rồi gửi đúng một turn khi job tới. Mục đích là bỏ 5–18s khởi động khỏi thời gian chờ của mỗi lần gọi.

Bên dùng đầu tiên là faster-learning. Lý do, số đo và thiết kế đầy đủ nằm ở `faster-learning/docs/openspecs/ai-cli-warm-pool/` (proposal, specs, design, tasks). File này chỉ ghi hợp đồng của thư viện.

## Constraints
- **Một session chạy đúng một turn.** Gọi `Turn` lần thứ hai trả `ErrSessionUsed`. Spike cho thấy agy giữ ngữ cảnh qua các turn, nên dùng lại process sẽ rò dữ liệu giữa các job.
- Mỗi `Start` có `HOME`/state riêng. Credential của account này không bao giờ thấy được ở session của account khác. Hiện thực trước cho `sandbox/host` (bên dùng đầu tiên — faster-learning — chỉ wire runtime này). `sandbox/docker` đã có sẵn trong repo, implement `sandbox.Runtime`; implement `sandbox.SessionRuntime` cho nó (một container cho mỗi process) là việc riêng, chưa cần ở đây.
- Dùng pipe, không dùng PTY. `RunInteractive` không phù hợp vì echo input và đổi `\n` thành `\r\n`, làm hỏng NDJSON.
- Chỉ thêm API, không đổi `Runner.Run`. Phát hành dưới dạng `v0.3.0` (tag để dành tới khi có bên dùng thật — xem faster-learning Phase 2).
- `Turn` trả JSON thô của dòng kết quả, **không** tự phân loại bằng `classify`/redact: giữa các turn session vẫn đang sống, chưa có `sandbox.CommandResult` đầy đủ (exit code, toàn bộ stderr) mà `classify` cần. Caller tự áp parser/luật auth-quota của riêng nó lên `TurnResult.Output`, đúng cách nó đã làm với output one-shot của `Run`.
- Profile có `SessionInput == ""` (ví dụ codex) thì `StartSession` trả `profile.ErrStreamingUnsupported`.

## API (đã hiện thực 2026-10-31)

```go
// sandbox
type SessionRuntime interface {
    Start(ctx context.Context, req CommandRequest) (Process, error)
}
type Process interface {
    Stdin() io.WriteCloser
    Stdout() io.Reader
    Wait() (*CommandResult, error)
    Kill(reason string) error
}

// profile
type Profile struct {
    // …
    SessionArgs  []string // flags that keep the CLI reading turns from stdin
    SessionInput string   // "agy" | "claude" | ""
}

// run
func (r *Runner) StartSession(ctx context.Context, req SessionRequest) (*Session, error)
func (s *Session) Ready(ctx context.Context) error
func (s *Session) Turn(ctx context.Context, prompt string) (*TurnResult, error)
func (s *Session) Close() (*sandbox.CommandResult, error)

type TurnResult struct {
    Output  []byte // the CLI's own raw result line, unparsed
    Latency time.Duration
}
```

Tên khác với bản nháp đầu (`StreamInput`/`WarmArgs` → `SessionInput`/`SessionArgs`): `StreamInput` trùng ý với `StreamArgs` đã có sẵn cho cơ chế streaming progress-event (một chiều, cho UI); đổi tên để không nhầm hai cơ chế.

Định dạng của agy (đã chạy thử 2026-10-31):
- Cờ: `--input-format stream-json --output-format stream-json -p ""`
- Input: `{"event":"user","message":{"content":"<prompt>"}}`
- Sẵn sàng: sự kiện `{"event":"init",…}`
- Kết quả: `{"event":"result","result":{"status","response","usage",…}}`

Định dạng của claude (đã chạy thử 2026-10-31, xem `run/stream/contract_cli_test.go`):
- Cờ: `--print --input-format stream-json --output-format stream-json --verbose --strict-mcp-config --tools ""`
- Input: `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"<prompt>"}]}}`
- Kết quả: dòng cuối của mỗi turn là `{"type":"result","is_error":…,"result":"<trả lời>",…}` — đúng dạng envelope mà `parseOutput` của one-shot (faster-learning) đã đọc được, nên không cần parser output riêng cho claude.

**Khác biệt quan trọng so với agy:** claude **không in gì cả, kể cả dòng `init`, trước khi có dữ liệu trên stdin** (đã xác nhận: chờ 15s không gửi gì → không ra một byte nào; gửi turn sau khi đã chờ 5s → dòng `init` xuất hiện chỉ ~170ms sau). Vì vậy `Session.Ready()` không có nghĩa giống nhau ở mọi CLI:
- agy: `Ready()` chờ đúng dòng `{"event":"init",…}`.
- claude: `Ready()` chỉ xác nhận process đã `Start()` và còn sống (chưa exit), không chờ dòng nào. Việc khởi động cục bộ của claude (nạp skills, settings, hooks) vẫn chạy ngầm trong lúc chờ, không phụ thuộc vào stdin.

## Verify
- `go test ./sandbox/... ./run/... ./profile/...` đạt, gồm:
  - Session trên host với script echo giả: ghi 1 dòng thì đọc lại đúng dòng đó, không có `\r` và không có echo.
  - Hai `Start` với hai credential khác nhau thì có HOME khác nhau và không thấy file của nhau.
  - `Turn` lần 2 trả `ErrSessionUsed`. Process thoát trước khi `Ready()` xong thì `Ready()` báo lỗi.
  - `Kill` không để lại process con.
  - Dùng **một goroutine đọc dài hạn** đổ dòng vào channel, không spawn goroutine mới cho mỗi lần chờ — spawn-per-wait từng làm treo test khi một goroutine bị bỏ lại sau timeout vẫn gọi `Scan()` đồng thời với lần chờ kế tiếp.
- `go test -tags cli ./run/stream/...` đạt với agy và claude thật trên máy có đăng nhập (đã chạy 2026-10-31, cả hai pass).
