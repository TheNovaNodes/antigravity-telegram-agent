# Contributing to Antigravity Telegram Agent

First off, thanks for taking the time to contribute! 🎉

## Development Workflow
This repository follows the **Strict Git Flow (ПРАВИЛА КРОВИ)**:
1. **Never commit directly to `master`.**
2. **Never fix bugs directly in production environments.**
3. Create a descriptive branch (e.g., `feature/awesome-thing` or `fix/nasty-bug`).
4. Submit a Pull Request targeting `master`.
5. Wait for automated CI checks (Go build, Vet, Test) to pass.
6. Request a code review from a maintainer.

## Local Setup
### Prerequisites
- Go 1.22+
- SQLite3
- Antigravity CLI binary (`agy`) installed in your `PATH` or at `~/.local/bin/agy`.

### Step 1: Clone the Repo
```bash
git clone https://github.com/TheNovaNodes/antigravity-go-tg-bot-agent.git
cd antigravity-go-tg-bot-agent
```

### Step 2: Configure Environment
Create a `.env` file in the root directory:
```env
BOT_TOKENS="your_telegram_bot_token_here"
ALLOWED_ADMIN_IDS="your_telegram_user_id_here"
ELEVENLABS_API_KEY="your_optional_elevenlabs_key_here"
```

### Step 3: Run the Bot
```bash
go run .
```

## Testing
Always run the test suite and race detector before submitting a Pull Request:
```bash
make race        # Run test suite with Go data race detector
make coverage    # Verify statements coverage (enforce >= 80%)
```
If you are adding a new feature or bugfix, please include corresponding unit tests to maintain or improve code coverage.
