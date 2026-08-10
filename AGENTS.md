# Agents Guidelines

This repository follows standard practices for automated agents working on the Antigravity Agent Ecosystem.

1. **Language:** All documentation must be written in high-quality English.
2. **Quality:** Ensure testing is performed via `pytest` for all major modifications.
3. **Ecosystem Role:** Remember that this project integrates with TheNovaNodes and the Antigravity Agent Ecosystem. Avoid making changes that break the PTY-architecture or MCP integration.
4. **Testing:** Run `python -m pytest` to verify the test suite. If an agent adds new functionality, corresponding tests should be added.
