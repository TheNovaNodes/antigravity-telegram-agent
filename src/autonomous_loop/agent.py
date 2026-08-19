import abc
from typing import Dict, Any, List, Optional
import json
from src.autonomous_loop.patch_validator import AgentPatch

class AgentResult:
    def __init__(self, patch: str, summary: str, proposed_tests: List[str], structured_patches: Optional[List[AgentPatch]] = None):
        self.patch = patch # Unified diff fallback
        self.structured_patches = structured_patches # List of AgentPatch
        self.summary = summary
        self.proposed_tests = proposed_tests

class AgentAdapter(abc.ABC):
    """Abstract interface for LLM Agents used in Autonomous Loop."""
    
    @abc.abstractmethod
    def run(self, task_context: Dict[str, Any], attempt: int, history: list) -> AgentResult:
        pass

import subprocess
import re

class GeminiAgentAdapter(AgentAdapter):
    """Calls the real LLM using Antigravity CLI."""
    
    def run(self, task_context: Dict[str, Any], attempt: int, history: list) -> AgentResult:
        prompt = self._build_prompt(task_context, attempt, history)
        
        # Use agy CLI to invoke the real LLM agent
        res = subprocess.run(
            ["/root/.local/bin/agy", "--print", prompt, "--dangerously-skip-permissions"],
            capture_output=True,
            text=True
        )
        output = res.stdout
        
        # Extract structured patches
        structured_patches = None
        json_match = re.search(r'```json\n(.*?)\n```', output, re.DOTALL)
        if json_match:
            try:
                data = json.loads(json_match.group(1))
                if isinstance(data, list):
                    structured_patches = []
                    for item in data:
                        structured_patches.append(AgentPatch(
                            operation=item.get("operation"),
                            path=item.get("path"),
                            old_text=item.get("old_text"),
                            new_text=item.get("new_text")
                        ))
            except json.JSONDecodeError:
                pass

        # Extract unified diff fallback
        patch_match = re.search(r'```(?:diff|patch)\n(.*?)\n```', output, re.DOTALL)
        patch = patch_match.group(1).strip() if patch_match else ""
        patch = self._clean_diff(patch)
        
        return AgentResult(patch=patch, summary=output[:500], proposed_tests=[], structured_patches=structured_patches)

    def _clean_diff(self, diff: str) -> str:
        """Fixes missing leading spaces on empty context lines in LLM unified diffs."""
        lines = diff.split('\n')
        cleaned = []
        for line in lines:
            if line == '':
                cleaned.append(' ')
            else:
                cleaned.append(line)
        return '\n'.join(cleaned) + '\n'
        
    def _build_prompt(self, task_context: Dict[str, Any], attempt: int, history: list) -> str:
        prompt = f"""
You are an Autonomous Repair Agent. Your task is to fix a bug in the repository.

TASK INSTRUCTIONS:
{task_context.get('description', '')}

RULES:
1. You MUST return a structured patch in a ```json block containing a JSON array of operations.
   Example format:
   ```json
   [
     {{
       "operation": "REPLACE_EXACT",
       "path": "src/example.py",
       "old_text": "def existing():\\n    pass",
       "new_text": "def existing():\\n    return True"
     }}
   ]
   ```
2. Allowed operations: CREATE_FILE, REPLACE_EXACT, INSERT_AFTER, INSERT_BEFORE.
3. For REPLACE_EXACT, old_text MUST exactly match the file content (count == 1). 
4. You MUST NOT modify existing tests. You can ONLY create new tests in tests/test_agent_repair_<id>.py
5. Optionally, you may also output a unified diff inside a ```diff block as a fallback.
"""
        if attempt > 1:
            last = history[-1]
            prompt += f"\n\nPREVIOUS ATTEMPT FAILED.\nFailure Diagnostic:\n{last.get('diagnostic', 'Unknown error')}\n\nAnalyze the failure and provide a corrected patch. Remember that old_text MUST match exactly 1 occurrence in the file."
            
        return prompt
