import uuid
import time
import logging
from typing import Dict, Any, List
from .sandbox import Sandbox
from .verification import VerificationEngine, DiagnosticEngine

logger = logging.getLogger(__name__)

class BoundedAutonomousLoop:
    """The State Machine Orchestrator for the Self-Healing Loop."""
    
    def __init__(self, repo_path: str):
        self.repo_path = repo_path
        self.task_id = str(uuid.uuid4())[:8]
        self.state = "CREATED"
        self.history = []
        self.seen_fingerprints = set()
        self.max_retries = 3

    def run_cycle(self, agent_patch_callback) -> Dict[str, Any]:
        """Runs the autonomous loop using the provided callback to get the patch."""
        logger.info(f"[Task {self.task_id}] State: {self.state}")
        
        with Sandbox(self.repo_path, self.task_id) as sandbox:
            self.state = "SANDBOX_READY"
            
            for attempt in range(1, self.max_retries + 1):
                logger.info(f"--- Attempt {attempt} ---")
                self.state = "AGENT_RUNNING"
                
                # Mock calling the LLM Agent
                # In real flow, we'd pass diagnostic to the agent
                patch_content = agent_patch_callback(attempt, self.history)
                
                self.state = "PATCH_RECEIVED"
                
                # Apply Patch
                if not sandbox.apply_patch(patch_content):
                    self.state = "DIAGNOSING"
                    self.history.append({"attempt": attempt, "status": "FAIL", "reason": "Patch failed to apply"})
                    continue
                
                self.state = "VERIFYING"
                verifier = VerificationEngine(str(sandbox.worktree_path))
                result = verifier.verify()
                
                if result["status"] == "SECURITY_VIOLATION":
                    self.state = "BLOCK"
                    logger.error(f"Security Violation: {result['reason']}")
                    return {"final_state": self.state, "history": self.history}
                
                if result["status"] == "PASS":
                    self.state = "CANDIDATE"
                    diff = sandbox.collect_diff()
                    logger.info("Verification PASSED!")
                    return {"final_state": self.state, "patch": diff, "history": self.history}
                
                # FAIL
                self.state = "DIAGNOSING"
                diagnostic = DiagnosticEngine.extract(result["output"])
                fp = diagnostic["fingerprint"]
                
                logger.info(f"Verification FAILED. Fingerprint: {fp}")
                
                if fp in self.seen_fingerprints:
                    self.state = "ESCALATE"
                    logger.error("Repeated failure detected. Loop terminated.")
                    self.history.append({"attempt": attempt, "status": "FAIL", "fingerprint": fp, "reason": "REPEATED_FAILURE"})
                    break
                
                self.seen_fingerprints.add(fp)
                self.history.append({"attempt": attempt, "status": "FAIL", "fingerprint": fp, "diagnostic": diagnostic["relevant_lines"]})
                
                self.state = "RETRY"
            
            if self.state == "RETRY":
                self.state = "ESCALATE"
                
        return {"final_state": self.state, "history": self.history}
