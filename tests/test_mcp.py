import unittest
from pathlib import Path
import tempfile
import os

from src.mcp_config import MCPConfigManager
from src.mcp_manager import MCPManager


class TestMCPIntegration(unittest.TestCase):

    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        self.config_file = Path(self.temp_dir.name) / "mcp_config.json"

    def tearDown(self):
        self.temp_dir.cleanup()

    def test_mcp_config_defaults(self):
        manager = MCPConfigManager(config_path=self.config_file)
        self.assertTrue(manager.config["enabled"])
        self.assertIn("anythingllm", manager.config["servers"])
        self.assertIn("searxng", manager.config["servers"])
        self.assertIn("nextcloud", manager.config["servers"])
        self.assertIn("google-jules-doctormes", manager.config["servers"])
        self.assertIn("google-jules-novanodes", manager.config["servers"])

    def test_mcp_toggle_server(self):
        manager = MCPConfigManager(config_path=self.config_file)
        initial_state = manager.get_server("anythingllm")["enabled"]
        new_state = manager.toggle_server("anythingllm")
        self.assertNotEqual(initial_state, new_state)
        self.assertEqual(manager.get_server("anythingllm")["enabled"], new_state)

    def test_generate_agy_mcp_settings(self):
        manager = MCPConfigManager(config_path=self.config_file)
        settings = manager.generate_agy_mcp_settings()
        self.assertIn("mcpServers", settings)
        mcp_servers = settings["mcpServers"]
        self.assertIn("nova-anythingllm-mcp", mcp_servers)
        self.assertIn("nova-searxng-mcp", mcp_servers)
        self.assertIn("nextcloud-crm", mcp_servers)
        self.assertTrue(mcp_servers["nextcloud-crm"]["url"].endswith("/mcp/sse"))
        self.assertIn("google-jules-doctormes", mcp_servers)

    def test_mcp_manager_status_report(self):
        config_manager = MCPConfigManager(config_path=self.config_file)
        manager = MCPManager(config_manager=config_manager)
        report = manager.get_status_report()
        self.assertIn("AnythingLLM", report)
        self.assertIn("SearXNG", report)
        self.assertIn("Nextcloud", report)
        self.assertIn("Google Jules AI Agent", report)
        self.assertIn("[DATA]", report)

    def test_health_check_all(self):
        import asyncio
        config_manager = MCPConfigManager(config_path=self.config_file)
        manager = MCPManager(config_manager=config_manager)
        results = asyncio.run(manager.health_check_all())
        self.assertIn("anythingllm", results)
        self.assertIn("searxng", results)
        self.assertIn("nextcloud", results)
        self.assertIn("google-jules-doctormes", results)
        self.assertIn("google-jules-novanodes", results)
        self.assertIn("ok", results["anythingllm"])
        self.assertIn("status", results["anythingllm"])


if __name__ == "__main__":
    unittest.main()
