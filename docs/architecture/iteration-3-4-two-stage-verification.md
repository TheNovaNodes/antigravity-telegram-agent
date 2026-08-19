# Iteration 3.4 & 3.5: Trusted Two-Stage Verification & Resource Bounding

## Verdict: GO (Production Ready)

### The Problem
In previous iterations, the Orchestrator ran untrusted AI-generated code in the same Python process as the Verification Engine (`pytest`). This allowed adversarial agents to:
1. Use `os._exit(0)` to kill the test runner and simulate success.
2. Monkey-patch `pytest` internals to forcefully report a False PASS.
3. Traverse the host filesystem using `shutil` inside the AST.
4. Exhaust Orchestrator memory and host disk space via infinite `stdout` streams or massive file creation.

### The Solution: Architecture Redesign
We completely rebuilt the boundary between Execution and Verification.

1. **Two-Stage Clean Environment (Phase 2 Isolation)**
   - The sandbox runs agent tests and generates repairs.
   - Before `VerificationEngine` runs the *Protected Tests*, it **forcibly deletes all agent-provided test files** (`test_agent_repair_*.py`).
   - This physically prevents the agent from injecting monkey-patches into the verification run.

2. **Strict Docker Resource Bounding**
   - The test runner executes inside an unprivileged Docker container (`user 1000`).
   - `--read-only` prevents the agent from filling the host filesystem.
   - `--tmpfs /tmp:rw,size=50m` enforces a strict 50MB limit on temporary files.
   - `--pids-limit 100` stops fork bombs.
   - `--log-driver=none` disables Docker JSON logging to prevent host disk exhaustion via infinite stdout.

3. **Orchestrator RAM Protection**
   - `subprocess.run(capture_output=True)` is highly vulnerable to `stdout` floods (it buffers infinitely into RAM).
   - We modified `VerificationEngine` to pipe `stdout` to a `tempfile` and read exactly the first 256KB, immediately truncating any infinite streams and protecting the Orchestrator from OOM kills.

4. **AST Hardening**
   - We patched the AST `SecurityASTVisitor` to strictly block `importlib`, `__import__`, `eval`, `exec`, and `__builtins__`, neutralizing any attempts to bypass the `os` and `sys` bans.
   - Path traversal in `PatchValidator` was fixed by enforcing `Path.is_relative_to()`.

### Security Audit Results
All adversarial tests failed to breach the sandbox:
- `os._exit(0)`: Blocked by strict AST limits.
- `monkey_patch`: Blocked because agent tests are physically deleted before verification.
- `path_traversal`: Blocked by `is_relative_to()`.
- `giant_stdout`: Blocked by Docker 256KB read limit (Orchestrator RAM survived).
- `infinite_loop`: Blocked by Docker 30s timeout.
- `big_file`: Blocked by `tmpfs` 50MB quota.
- `many_files`: Blocked by `tmpfs` inode limits.

### Legitimacy Test
A legitimate bug fix simulating standard agent behavior successfully passed Phase 1 (AST validation), Phase 2 (Protected Tests in Docker), and correctly evaluated the fix, ensuring standard functionality is unimpeded.

**Iteration 3 BAEL is strictly isolated, bounded, and hardened. GO for Production.**
