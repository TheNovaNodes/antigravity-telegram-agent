import re

with open("agysessionsstarter.go", "r") as f:
    text = f.read()

# Add getAgentsDir function before getSession
helper = """
func getAgentsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/root/.agents"
	}
	return filepath.Join(home, ".agents")
}

func getSession"""

text = text.replace("func getSession", helper)

# Replace lines
text = text.replace('Workspace:    fmt.Sprintf("/root/.agents/%s", botName),', 'Workspace:    filepath.Join(getAgentsDir(), botName),')
text = text.replace('"--add-dir", "/root/.agents/common",', '"--add-dir", filepath.Join(getAgentsDir(), "common"),')
text = text.replace('"--add-dir", "/root/.agents/" + s.BotName,', '"--add-dir", filepath.Join(getAgentsDir(), s.BotName),')
text = text.replace('agentDir := fmt.Sprintf("/root/.agents/%s", s.BotName)', 'agentDir := filepath.Join(getAgentsDir(), s.BotName)')
text = text.replace('downloadDir := fmt.Sprintf("/root/.agents/%s/scratch/downloads", botName)', 'downloadDir := filepath.Join(getAgentsDir(), botName, "scratch", "downloads")')
text = text.replace("workspace TEXT DEFAULT '/root/.agents',", "workspace TEXT DEFAULT '',")

with open("agysessionsstarter.go", "w") as f:
    f.write(text)
