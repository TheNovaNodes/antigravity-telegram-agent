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
        self.assertIn("anythingllm-memory", mcp_servers)
        self.assertIn("searxng-search", mcp_servers)
        self.assertIn("nextcloud-crm", mcp_servers)

    def test_mcp_manager_status_report(self):
        config_manager = MCPConfigManager(config_path=self.config_file)
        manager = MCPManager(manager=config_manager)
        report = manager.get_status_report()
        self.assertIn("AnythingLLM Semantic Memory", report)
        self.assertIn("SearXNG Web Search", report)
        self.assertIn("Nextcloud User CRM", report)


if __name__ == "__main__":
    unittest.main()
