import os
import logging
import aiohttp
from typing import Dict, Any, Optional

logger = logging.getLogger(__name__)

class JulesClient:
    """Client for interacting with the Google Jules REST API."""
    
    BASE_URL = "https://jules.googleapis.com/v1alpha"

    def __init__(self, api_key: Optional[str] = None):
        self.api_key = api_key or os.getenv("JULES_API_KEY")
        if not self.api_key:
            logger.warning("JULES_API_KEY is not set.")
            
    async def _request(self, method: str, endpoint: str, json_data: dict = None) -> Dict[str, Any]:
        """Helper to make HTTP requests to the Jules API."""
        if not self.api_key:
            raise ValueError("Cannot make request: JULES_API_KEY is missing.")
            
        headers = {
            "Content-Type": "application/json",
            "x-goog-api-key": self.api_key
        }
        
        url = f"{self.BASE_URL}/{endpoint.lstrip('/')}"
        
        async with aiohttp.ClientSession() as session:
            async with session.request(method, url, headers=headers, json=json_data) as response:
                if not response.ok:
                    error_text = await response.text()
                    logger.error(f"Jules API Error {response.status}: {error_text}")
                    response.raise_for_status()
                return await response.json()

    async def list_sources(self) -> Dict[str, Any]:
        """List available sources (e.g. connected GitHub repositories)."""
        return await self._request("GET", "/sources")

    async def create_session(self, source_name: str, prompt: str) -> Dict[str, Any]:
        """Create a new Jules session for a specific source."""
        payload = {
            "source": source_name,
            "prompt": prompt
        }
        return await self._request("POST", "/sessions", json_data=payload)

    async def get_session(self, session_name: str) -> Dict[str, Any]:
        """Get the status of a specific Jules session."""
        session_name = session_name.lstrip('/')
        if not session_name.startswith('sessions/'):
            session_name = f"sessions/{session_name}"
        return await self._request("GET", f"/{session_name}")

    async def get_session_activities(self, session_name: str) -> Dict[str, Any]:
        """List activities/steps for a specific Jules session."""
        session_name = session_name.lstrip('/')
        if not session_name.startswith('sessions/'):
            session_name = f"sessions/{session_name}"
        return await self._request("GET", f"/{session_name}/activities")

    async def list_artifacts(self, session_name: str) -> Dict[str, Any]:
        """List artifacts produced by a Jules session."""
        session_name = session_name.lstrip('/')
        if not session_name.startswith('sessions/'):
            session_name = f"sessions/{session_name}"
        return await self._request("GET", f"/{session_name}/artifacts")

    async def get_artifact_content(self, artifact_name: str) -> Dict[str, Any]:
        """Get contents of a specific Jules artifact."""
        artifact_name = artifact_name.lstrip('/')
        return await self._request("GET", f"/{artifact_name}")

# Simple test block to run directly
if __name__ == "__main__":
    import asyncio
    from dotenv import load_dotenv
    
    async def test():
        load_dotenv()
        client = JulesClient()
        if not client.api_key:
            print("Error: JULES_API_KEY is not set in .env")
            return
            
        print("Testing Jules API...")
        try:
            sources = await client.list_sources()
            print("Successfully fetched sources!")
            print(sources)
        except Exception as e:
            print(f"Failed to fetch sources: {e}")
            
    asyncio.run(test())
