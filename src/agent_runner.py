import asyncio
import json
import logging
import os
import re
from typing import Optional, AsyncGenerator
from pathlib import Path

from src.config import AGY_BINARY_PATH

logger = logging.getLogger(__name__)

AVAILABLE_MODELS = {
    "gemini-flash-high": "gemini-3.6-flash-high",
    "gemini-flash-medium": "gemini-3.6-flash-medium",
    "gemini-flash-low": "gemini-3.6-flash-low",
    "gemini-pro-high": "gemini-3.1-pro-high",
    "gemini-pro-low": "gemini-3.1-pro-low",
    "claude-sonnet": "claude-sonnet-4-6",
    "claude-opus": "claude-opus-4-6-thinking",
    "gpt-oss": "gpt-oss-120b-medium"
}

AVAILABLE_EFFORTS = ["low", "medium", "high"]
AVAILABLE_MODES = {
    "default": "Default",
    "accept-edits": "Auto-Accept Edits",
    "plan": "Planner Mode"
}

def get_active_account_email() -> str:
    """Reads the active email from the Antigravity CLI session token."""
    token_path = Path.home() / ".gemini" / "antigravity-cli" / "antigravity-oauth-token"
    if not token_path.exists():
        return "Not Logged In"
    try:
        content = json.loads(token_path.read_text())
        return content.get("email", "Unknown Account")
    except Exception:
        return "Unknown Account"

class AgySession:
    """Manages conversational state and executes agy CLI dynamically."""

    def __init__(
        self,
        chat_id: int,
        model_name: str = "gemini-3.6-flash-low",
        effort: str = "low",
        mode: str = "default",
        conversation_id: Optional[str] = None,
        workspace: Optional[str] = None
    ):
        self.chat_id = chat_id
        self.model_name = model_name
        self.effort = effort
        self.mode = mode
        self.conversation_id = conversation_id
        self.workspace = workspace
        self._lock = asyncio.Lock()

    def set_model(self, model_key: str) -> bool:
        if model_key in AVAILABLE_MODELS:
            new_model = AVAILABLE_MODELS[model_key]
        elif model_key in AVAILABLE_MODELS.values():
            new_model = model_key
        else:
            return False

        if self.model_name != new_model:
            self.model_name = new_model
            logger.info(f"Switching model for chat_id={self.chat_id} to {self.model_name}")
            from src.db import save_user_session
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def set_effort(self, effort_level: str) -> bool:
        if effort_level in AVAILABLE_EFFORTS:
            if self.effort != effort_level:
                self.effort = effort_level
                logger.info(f"Switching effort for chat_id={self.chat_id} to {self.effort}")
                from src.db import save_user_session
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
            return True
        return False

    def set_mode(self, mode_key: str) -> bool:
        if mode_key in AVAILABLE_MODES:
            if self.mode != mode_key:
                self.mode = mode_key
                logger.info(f"Switching mode for chat_id={self.chat_id} to {self.mode}")
                from src.db import save_user_session
                save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
            return True
        return False

    def set_conversation(self, conversation_id: Optional[str]) -> bool:
        if self.conversation_id != conversation_id:
            self.conversation_id = conversation_id
            logger.info(f"Switching conversation for chat_id={self.chat_id} to {conversation_id}")
            from src.db import save_user_session
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def set_workspace(self, workspace: Optional[str]) -> bool:
        if self.workspace != workspace:
            self.workspace = workspace
            logger.info(f"Switching workspace for chat_id={self.chat_id} to {workspace}")
            from src.db import save_user_session
            save_user_session(self.chat_id, self.model_name, self.effort, self.mode, self.conversation_id, self.workspace)
        return True

    def clear_context(self):
        """Resets conversation context."""
        self.conversation_id = None
        
    def close(self):
        """No-op. Compatibility method for PTY legacy code."""
        pass

    async def get_usage_info(self) -> str:
        """Returns quota usage strings."""
        return "Quotas and Usage API not supported in God Mode JSON stream."

    async def stream_response(self, prompt: str) -> AsyncGenerator[str, None]:
        """Streams JSON output from agy CLI."""
        async with self._lock:
            # Check auth status
            email = get_active_account_email()
            if email == "Not Logged In":
                yield "⚠️ <b>Agent lost authorization!</b>\nPlease log in to the server via SSH as root and run <code>agy auth login</code>, then repeat the request."
                return

            args = [
                AGY_BINARY_PATH,
                "--print", prompt,
                "--output-format", "stream-json",
                "--dangerously-skip-permissions",
                "--model", self.model_name,
                "--effort", self.effort
            ]
            if self.mode != "default":
                args.extend(["--mode", self.mode])
            
            if self.conversation_id:
                if self.conversation_id == "latest":
                    args.append("--continue")
                else:
                    args.extend(["--conversation", self.conversation_id])

            logger.info(f"Running JSON stream for chat_id={self.chat_id}: {' '.join(args)}")

            env = os.environ.copy()
            # To ensure the process returns JSON in utf-8
            env["PYTHONIOENCODING"] = "utf-8"
            
            from src.mcp_manager import mcp_manager
            mcp_env = mcp_manager.get_env_dict()
            env.update(mcp_env)

            proc = await asyncio.create_subprocess_exec(
                *args,
                cwd=self.workspace,
                env=env,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE
            )

            if proc.stdout is None:
                yield "⚠️ <b>Internal error: Failed to capture agent stdout.</b>"
                return
                
            try:
                while True:
                    line = await proc.stdout.readline()
                    if not line:
                        break
                        
                    line_str = line.decode('utf-8').strip()
                    if not line_str:
                        continue
                        
                    try:
                        data = json.loads(line_str)
                        if data.get("event") == "init" and "conversation_id" in data.get("init", {}):
                            pass
                        elif data.get("event") == "init" and "conversation_id" in data:
                            new_conv = data["conversation_id"]
                            if self.conversation_id != new_conv and self.conversation_id != "latest":
                                self.set_conversation(new_conv)
                                
                        if data.get("event") == "step_update":
                            step = data.get("step_update", {})
                            if step.get("step_type") == "agent_response" and "text_delta" in step:
                                yield step["text_delta"]
                                
                        if data.get("event") == "error":
                            err_msg = data.get("error", {}).get("message", "Unknown error")
                            yield f"\n⚠️ <b>Error:</b> {err_msg}"
                            
                    except json.JSONDecodeError:
                        logger.warning(f"Non-JSON line from agy: {line_str}")
            finally:
                await proc.wait()

    async def get_response(self, prompt: str) -> str:
        """Returns the full response synchronously by consuming the stream."""
        response = ""
        async for chunk in self.stream_response(prompt):
            response += chunk
        return response
