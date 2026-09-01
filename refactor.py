import re

with open("agysessionsstarter.go", "r") as f:
    text = f.read()

# 1. Update getSession definition to compute sessionKey
text = re.sub(
    r'(func getSession\(botName string, user User\) \*AgySession \{)',
    r'\1\n\tsessionKey := fmt.Sprintf("%s:%d", botName, user.ID)',
    text
)

# 2. Update handleUpdate to compute sessionKey right after botName
text = re.sub(
    r'(botName := bot\.Self\.UserName\s*\n\s*user := getUser\(db, userID, botName\))',
    r'\1\n\tsessionKey := fmt.Sprintf("%s:%d", botName, userID)',
    text
)

# 3. In getSession and handleUpdate, replace globalSessions uses
# Luckily, globalSessions is ONLY used in these two functions.
text = text.replace('globalSessions[botName]', 'globalSessions[sessionKey]')
text = text.replace('delete(globalSessions, botName)', 'delete(globalSessions, sessionKey)')

with open("agysessionsstarter.go", "w") as f:
    f.write(text)
