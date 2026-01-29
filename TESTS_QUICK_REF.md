# Quick Reference: Test Files

## Directory Structure

```
pkg/
├── claudeapi/
│   ├── client_test.go         (321 lines) - HTTP client & API calls
│   ├── streaming_test.go      (331 lines) - SSE streaming
│   ├── conversations_test.go  (341 lines) - Context management
│   ├── models_test.go         (285 lines) - Model validation
│   └── errors_test.go         (367 lines) - Error handling
└── connector/
    ├── connector_test.go      (291 lines) - Core connector
    ├── login_test.go          (299 lines) - Authentication
    └── config_test.go         (441 lines) - Configuration
```

## Test Command Quick Reference

```bash
# Run all tests
go test ./pkg/... -v

# Run specific package
go test ./pkg/claudeapi/... -v
go test ./pkg/connector/... -v

# Run single test file
go test ./pkg/claudeapi/ -v -run TestNewClient

# With coverage
go test ./pkg/... -cover -coverprofile=coverage.out
go tool cover -html=coverage.out

# With race detection
go test ./pkg/... -race

# Verbose output
go test ./pkg/... -v -count=1
```

## What Each Test File Covers

### client_test.go
- NewClient() creation
- CreateMessage() API calls
- API error responses (401, 429, 500)
- ValidateAPIKey()
- Context cancellation

### streaming_test.go
- CreateMessageStream()
- SSE event parsing
- Event types: message_start, content_block_delta, message_stop
- Stream error handling
- Context cancellation

### conversations_test.go
- NewConversationManager()
- AddMessage() (user/assistant)
- GetMessages()
- Clear()
- Message alternation
- Thread safety
- TrimToTokenLimit()

### models_test.go
- ValidateModel()
- GetModelMaxTokens()
- DefaultModel
- ValidModels list
- Model constants
- GetModelInfo()
- GetModelFamily()

### errors_test.go
- ParseAPIError()
- IsRateLimitError()
- IsAuthError()
- IsOverloadedError()
- IsInvalidRequestError()
- GetRetryAfter()
- Error interface compliance

### connector_test.go
- NewConnector()
- GetName() (bridge metadata)
- GetLoginFlows()
- GetDBMetaTypes()
- GetCapabilities()
- Metadata structures
- ID generation (MakeClaudeGhostID, etc.)

### login_test.go
- APIKeyLogin.Start()
- APIKeyLogin.SubmitUserInput()
- API key format validation (sk-ant-*)
- CreateLogin()
- API key storage

### config_test.go
- Config validation
- Model validation in config
- Temperature range (0.0-1.0)
- MaxTokens validation
- SystemPrompt
- ConversationMaxAge
- RateLimitPerMinute
- ExampleConfig YAML

## Expected Test States

### Current (RED - TDD Phase 1)
```
FAIL: undefined: NewClient
FAIL: undefined: CreateMessageRequest
FAIL: unknown field DefaultModel
```
All tests fail - implementation doesn't exist yet.

### After Implementation (GREEN - TDD Phase 2)
```
PASS: TestNewClient
PASS: TestCreateMessage
PASS: All tests
```
All tests pass - implementation complete.

### After Refactoring (GREEN - TDD Phase 3)
```
PASS: All tests (still passing after refactoring)
```
Code improved while maintaining green tests.

## Files That Need to Be Created

### For claudeapi tests to pass:
- pkg/claudeapi/types.go
- pkg/claudeapi/client.go
- pkg/claudeapi/streaming.go
- pkg/claudeapi/conversations.go
- pkg/claudeapi/models.go
- pkg/claudeapi/errors.go

### For connector tests to pass:
- Update pkg/connector/connector.go
- Update pkg/connector/config.go
- Update pkg/connector/login.go

## Key Test Patterns Used

### Table-Driven Tests
```go
tests := []struct {
    name        string
    input       Type
    expectError bool
}{
    {name: "case 1", input: ..., expectError: false},
    {name: "case 2", input: ..., expectError: true},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        // Test logic
    })
}
```

### Mock HTTP Server
```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    w.WriteHeader(http.StatusOK)
    json.NewEncoder(w).Encode(response)
}))
defer server.Close()
```

### Context Cancellation
```go
ctx, cancel := context.WithCancel(context.Background())
cancel() // Cancel immediately
_, err := client.CreateMessage(ctx, req)
// Should return context.Canceled error
```

## Test Quality Checklist

When implementing:
- [ ] All tests compile
- [ ] All tests pass
- [ ] No race conditions (`go test -race`)
- [ ] Coverage >80% (`go test -cover`)
- [ ] Tests run fast (<1s per test)
- [ ] Tests are isolated (no shared state)
- [ ] Error messages are clear

## Common Test Scenarios

### Testing Success Cases
```go
resp, err := client.CreateMessage(ctx, validRequest)
if err != nil {
    t.Fatalf("unexpected error: %v", err)
}
if resp == nil {
    t.Fatal("expected response, got nil")
}
```

### Testing Error Cases
```go
_, err := client.CreateMessage(ctx, invalidRequest)
if err == nil {
    t.Error("expected error, got nil")
}
if !strings.Contains(err.Error(), "expected message") {
    t.Errorf("wrong error: %v", err)
}
```

### Testing Validation
```go
if !ValidateModel("claude-3-5-sonnet-20241022") {
    t.Error("should accept valid model")
}
if ValidateModel("invalid-model") {
    t.Error("should reject invalid model")
}
```

## Notes

- Tests define the contract - don't change them during implementation
- Use `go test -v` to see which specific test is failing
- Use `go test -run TestName` to run a specific test
- All tests should be deterministic (no random values)
- Mock all external dependencies (HTTP, database, etc.)

---

**Created**: 2026-01-24  
**Status**: Tests written, implementation pending
