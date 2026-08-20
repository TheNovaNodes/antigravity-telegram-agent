# Manus Capacity Pool Architecture

Since Manus AI accounts have strict rate limits and our "Unlimited Window" is temporary (closing Aug 25, 2026), this skill uses a **Capacity Pool**.

1. The `MANUS_KEYS` environment variable contains a comma-separated list of API keys.
2. `dispatcher.py` hashes the task prompt to deterministically pick a key, or iterates through them if a `429 Too Many Requests` is encountered.
3. For private repositories, the dispatcher **MUST** use a specific key that has GitHub OAuth permissions for that repo, otherwise Manus will fallback to public web scraping and hallucinate.
