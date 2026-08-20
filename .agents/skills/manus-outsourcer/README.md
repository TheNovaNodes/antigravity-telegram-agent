# Manus Outsourcer Skill

This skill enables coding agents (like Antigravity) to delegate long-running or web-heavy tasks to Manus AI.
It implements a "Capacity Pool" using multiple Manus API keys and a round-robin `dispatcher.py` to circumvent rate limits.

## Installation
For use with Antigravity, place this folder in `.agents/skills/manus-outsourcer` within your repository.

## Usage (For Agents)
Agents should refer to `SKILL.md` for specific invocation instructions and execution pipelines.

## Structure
- `SKILL.md`: Core system prompt and mission control.
- `scripts/`: Contains `dispatcher.py` and `poller.py` for API interaction.
- `resources/`: Knowledge base for Manus AI constraints.
