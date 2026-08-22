import asyncio
import logging
import time
from typing import Optional
from aiogram import Router, types, Bot
from aiogram.filters import Command
from aiogram.types import Message, InlineKeyboardMarkup, InlineKeyboardButton, CallbackQuery
from aiogram.enums import ChatAction

from src.config import ALLOWED_USER_IDS
from src.session_manager import session_manager
from src.cli_runner import AVAILABLE_MODELS, AVAILABLE_EFFORTS, AVAILABLE_MODES, get_active_account_email
from src.mcp_config import MCPConfigManager
from src.mcp_manager import MCPManager
from src.audit import log_audit_event
from src.formatters import safe_html_truncate

from src.session_key import SessionKey

logger = logging.getLogger(__name__)
router = Router()

def get_session_key(event: types.TelegramObject, bot: Bot) -> SessionKey:
    """Helper to consistently extract composite SessionKey from Bot and Telegram event.
    Fails closed if bot or chat context is missing.
    """
    if not bot or not getattr(bot, "id", None):
        raise ValueError("Cannot resolve SessionKey: Bot instance missing or invalid bot.id")

    chat_id = None
    if isinstance(event, Message) and getattr(event, "chat", None):
        chat_id = event.chat.id
    elif isinstance(event, CallbackQuery) and event.message and getattr(event.message, "chat", None):
        chat_id = event.message.chat.id
    elif getattr(event, "chat", None):
        chat_id = event.chat.id

    if chat_id is None:
        raise ValueError("Cannot resolve SessionKey: Event missing valid chat context")

    return SessionKey(bot_id=bot.id, chat_id=chat_id)


def is_allowed(user_id: int) -> bool:
    if not ALLOWED_USER_IDS:
        logger.error("ALLOWED_USER_IDS is empty! Failing closed for security.")
        return False
    return user_id in ALLOWED_USER_IDS


def get_main_menu_keyboard(session) -> InlineKeyboardMarkup:
    buttons = [
        [
            InlineKeyboardButton(text=f"🤖 Model: {session.model_name}", callback_data="menu:models")
        ],
        [
            InlineKeyboardButton(text=f"🎯 Mode: {AVAILABLE_MODES.get(session.mode, session.mode)}", callback_data="menu:mode"),
            InlineKeyboardButton(text=f"📂 {session.workspace if session.workspace else 'Home Dir'}", callback_data="menu:workspace")
        ],
        [
            InlineKeyboardButton(text="💬 Resume / History", callback_data="menu:resume"),
            InlineKeyboardButton(text="🤖 Jules & Subagents", callback_data="menu:jules")
        ],
        [
            InlineKeyboardButton(text="🔌 MCP Gateways", callback_data="menu:mcp"),
            InlineKeyboardButton(text="📊 Account & Quotas", callback_data="menu:account")
        ],
        [
            InlineKeyboardButton(text="🛡️ Autonomous Sentinel (Auto-Pilot)", callback_data="sentinel:menu")
        ],
        [
            InlineKeyboardButton(text="🔄 Start New Session", callback_data="menu:reset")
        ]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

from src.conversations import get_available_conversations, get_conversation_title
from src.jules_monitor import ACTIVE_JULES_SESSIONS

def get_menu_text(session, is_start=False) -> str:
    active_session_title = "New Session (Isolated)"
    if session.conversation_id:
        if session.conversation_id == "latest":
            active_session_title = "Latest Active Session (--continue)"
        else:
            title = get_conversation_title(session.conversation_id, profile=session.profile)
            active_session_title = f"{title} ({session.conversation_id[:8]})" if title else f"ID: {session.conversation_id[:8]}"

    from src.config import AGY_BINARY_PATH
    import os
    agy_exists = os.path.exists(AGY_BINARY_PATH)
    email = get_active_account_email(session.profile)
    health_emoji = "✅" if agy_exists and email != "Not Logged In" and email != "" else "⚠️"

    header = "👋 <b>Welcome to Antigravity Telegram Agent!</b>\n\nI am your mobile interface to <b>Google Antigravity (agy)</b>. Send me a prompt, and I will execute it in your workspace.\n\n" if is_start else "🎛️ <b>Antigravity Telegram Agent Control Center</b>\n\n"
    
    text = (
        f"{header}"
        f"<b>System Status:</b> {health_emoji}\n"
        f"• CLI Binary: <code>{AGY_BINARY_PATH}</code>\n"
        f"• Authenticated as: <code>{email}</code>\n\n"
        f"💬 <b>Active Session:</b> <code>{active_session_title}</code>\n"
        f"🤖 <b>Model & Effort:</b> <code>{session.model_name}</code>\n"
        f"🎯 <b>Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n"
        f"📂 <b>Workspace:</b> <code>{session.workspace if session.workspace else 'Home Directory'}</code>\n\n"
    )
    
    if is_start:
        text += (
            "<b>Available Commands:</b>\n"
            "• /menu — Control Center\n"
            "• /cd — Change workspace directory\n"
            "• /resume — Resume saved conversation\n"
            "• /mcp — Manage MCP Servers\n"
            "• /models — Select AI Model & Reasoning Level\n"
            "• /mode — Execution Mode (Standard/Plan)\n"
            "• /reset — Reset active session\n"
        )
    else:
        text += "Select an option to configure:"
        
    return text


def get_models_keyboard() -> InlineKeyboardMarkup:
    buttons = []
    for alias, full_name in AVAILABLE_MODELS.items():
        buttons.append([InlineKeyboardButton(text=f"🤖 {full_name}", callback_data=f"set_model:{alias}")])
    buttons.append([InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


from src.profile import BotProfile

def get_resume_keyboard(current_conversation_id: str = None, profile: Optional[BotProfile] = None) -> InlineKeyboardMarkup:
    """Build resume keyboard with current session indicator and new session button."""
    conversations = get_available_conversations(limit=15, profile=profile)
    buttons = [
        [InlineKeyboardButton(text="🆕 New Session (Clean Chat)", callback_data="resume_set:new")],
        [InlineKeyboardButton(text="🔄 Resume Latest Session (--continue)", callback_data="resume_set:latest")]
    ]
    for conv in conversations:
        date_part = f" ({conv['date']})" if conv['date'] else ""
        is_current = current_conversation_id and conv['id'] == current_conversation_id
        marker = "✅ " if is_current else "💬 "
        short_id = conv['id'][:4]
        label = f"{marker}[{short_id}] {conv['summary'][:22]}{date_part}"
        buttons.append([InlineKeyboardButton(text=label, callback_data=f"resume_set:{conv['id']}")])

    buttons.append([InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_effort_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        [
            InlineKeyboardButton(text="⚡ Low (Fast)", callback_data="set_effort:low"),
            InlineKeyboardButton(text="🧠 Medium (Balanced)", callback_data="set_effort:medium"),
            InlineKeyboardButton(text="🚀 High (Deep)", callback_data="set_effort:high")
        ],
        [InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_mode_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        [InlineKeyboardButton(text="💬 Standard Chat", callback_data="set_mode:default")],
        [InlineKeyboardButton(text="📋 Planning Mode", callback_data="set_mode:plan")],
        [InlineKeyboardButton(text="⚡ Auto-Edits Mode", callback_data="set_mode:accept-edits")],
        [InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_mcp_keyboard(profile: Optional[BotProfile] = None) -> InlineKeyboardMarkup:
    mgr = MCPManager(MCPConfigManager(profile=profile))
    servers = mgr.config_manager.config.get("servers", {})
    buttons = []
    
    icons = {
        "anythingllm": "🧠 AnythingLLM (Memory)",
        "anythingllm-control": "⚙️ AnythingLLM (Control)",
        "searxng": "🔍 SearXNG (Search)",
        "searxng-control": "⚙️ SearXNG (Control)",
        "nextcloud": "💼 Nextcloud (CRM)",
        "nextcloud-control": "⚙️ Nextcloud (Control)",
        "google-jules-doctormes": "🤖 Jules (Doctormes)",
        "google-jules-novanodes": "🤖 Jules (TheNovaNodes)",
        "universal-pr-auditor": "🛡️ PR Auditor (Universal)"
    }

    for key, srv in servers.items():
        state_icon = "✅" if srv.get("enabled") else "❌"
        btn_text = icons.get(key, f"🔌 {srv.get('name', key)}")
        if len(btn_text) > 30:
            btn_text = btn_text[:27] + "..."
        buttons.append([InlineKeyboardButton(text=f"{btn_text}: {state_icon}", callback_data=f"toggle_mcp:{key}")])

    buttons.append([InlineKeyboardButton(text="🧪 Health Check", callback_data="mcp_health_check")])
    buttons.append([InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

@router.message(Command("start"), Command("help"))
async def cmd_start(message: Message):
    if not is_allowed(message.from_user.id):
        logger.warning(f"Unauthorized access attempt from user_id={message.from_user.id}")
        return

    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    await message.answer(
        get_menu_text(session, is_start=True),
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )

@router.message(Command("menu"))
async def cmd_menu(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    await message.answer(
        get_menu_text(session, is_start=False),
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )

@router.message(Command("mcp"))
async def cmd_mcp(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    mgr = MCPManager(MCPConfigManager(profile=session.profile))
    report = mgr.get_status_report()
    await message.answer(report, reply_markup=get_mcp_keyboard(profile=session.profile), parse_mode="HTML")

@router.message(Command("models"))
async def cmd_models(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    await message.answer(
        f"🎯 <b>Current Model:</b> <code>{session.model_name}</code>\n\n"
        "Select a model to switch:",
        reply_markup=get_models_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("effort"))
async def cmd_effort(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    await message.answer(
        f"⚙️ <b>Current Effort:</b> <code>{session.effort}</code>\n\n"
        "Select reasoning effort:",
        reply_markup=get_effort_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("mode"))
async def cmd_mode(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    mode_desc = AVAILABLE_MODES.get(session.mode, session.mode)
    await message.answer(
        f"🎯 <b>Current Mode:</b> <code>{mode_desc}</code>\n\n"
        "Select execution mode:",
        reply_markup=get_mode_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("reset"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session_manager.new_session(key)
    await message.answer("🔄 <b>Session state reset!</b> Next message will launch a fresh agy process.", parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("menu:"))
async def process_menu_navigation(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    action = callback_query.data.split(":")[1]
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)

    if action == "main":
        await callback_query.answer()
        await callback_query.message.edit_text(
            get_menu_text(session, is_start=False),
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )
    elif action == "mcp":
        await callback_query.answer()
        mgr = MCPManager(MCPConfigManager(profile=session.profile))
        report = mgr.get_status_report()
        await callback_query.message.edit_text(
            report,
            reply_markup=get_mcp_keyboard(profile=session.profile),
            parse_mode="HTML"
        )
    elif action == "models":
        await callback_query.answer()
        await callback_query.message.edit_text(
            f"🎯 <b>Current Model:</b> <code>{session.model_name}</code>\n\n"
            "Select a model to switch:",
            reply_markup=get_models_keyboard(),
            parse_mode="HTML"
        )
    elif action == "effort":
        await callback_query.answer()
        await callback_query.message.edit_text(
            f"⚙️ <b>Current Effort:</b> <code>{session.effort}</code>\n\n"
            "Select reasoning effort:",
            reply_markup=get_effort_keyboard(),
            parse_mode="HTML"
        )
    elif action == "mode":
        await callback_query.answer()
        mode_desc = AVAILABLE_MODES.get(session.mode, session.mode)
        await callback_query.message.edit_text(
            f"🎯 <b>Current Mode:</b> <code>{mode_desc}</code>\n\n"
            "Select execution mode:",
            reply_markup=get_mode_keyboard(),
            parse_mode="HTML"
        )
    elif action == "reset":
        session_manager.new_session(key)
        await callback_query.answer("Session reset!")
        await callback_query.message.edit_text(
            "✨ <b>Session reset complete!</b> Next message will launch a fresh conversation.",
            reply_markup=get_main_menu_keyboard(session_manager.get_session(key)),
            parse_mode="HTML"
        )
    elif action == "workspace":
        from src.workspace_utils import get_workspace_keyboard
        await callback_query.message.edit_text(
            f"📂 <b>Current Workspace:</b> <code>{session.workspace if session.workspace else 'Home Directory (/root)'}</code>\n\n"
            "Select a project/folder to pin for the session:",
            reply_markup=get_workspace_keyboard(),
            parse_mode="HTML"
        )
    elif action == "resume":
        kb = get_resume_keyboard(session.conversation_id, profile=session.profile)
        await callback_query.message.edit_text("💬 <b>Select a conversation to resume:</b>", reply_markup=kb, parse_mode="HTML")

    elif action == "jules":
        from src.jules_monitor import ACTIVE_JULES_SESSIONS
        sessions_str = ""
        if ACTIVE_JULES_SESSIONS:
            sessions_str = "\n".join([f"• <code>{s}</code> (Chat: {c})" for s, c in ACTIVE_JULES_SESSIONS.items()])
        else:
            sessions_str = "<i>No active Jules sessions monitored.</i>"
            
        text = (
            f"🤖 <b>Jules Subagent Dashboard</b>\n\n"
            f"Active GitHub Task Sessions:\n{sessions_str}\n\n"
            f"💡 <i>Jules automatically runs background tasks on GitHub and reports patches here.</i>"
        )
        kb = InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]])
        await callback_query.message.edit_text(text, reply_markup=kb, parse_mode="HTML")
    elif action == "account":
        email = get_active_account_email(session.profile)
        kb = InlineKeyboardMarkup(inline_keyboard=[
            [InlineKeyboardButton(text="📊 View API Quotas & Usage", callback_data="menu:usage")],
            [InlineKeyboardButton(text="🔄 Reconnect Authorization (Hot Reload)", callback_data="account:reload")],
            [InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]
        ])
        text = (
            f"🔑 <b>Account & API Quotas Center</b>\n\n"
            f"👤 <b>Authenticated Account:</b> <code>{email}</code>\n"
            f"⚙️ <b>Auth Pickup:</b> Automatic (Hot Reload)\n\n"
            f"Select an action below:"
        )
        await callback_query.message.edit_text(text, reply_markup=kb, parse_mode="HTML")
    elif action == "usage":
        await callback_query.bot.send_chat_action(chat_id=callback_query.message.chat.id, action=ChatAction.TYPING)
        await callback_query.answer("Requesting full quota information...")
        key = get_session_key(callback_query, callback_query.bot)
        session = session_manager.get_session(key)
        formatted = await session.get_usage_info()
        kb = InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]])
        try:
            await callback_query.message.edit_text(formatted, reply_markup=kb, parse_mode="HTML")
        except Exception:
            await safe_edit_text(callback_query.message, formatted)
    elif action == "reboot":
        await callback_query.message.edit_text("⏳ <i>Starting agy... please wait</i>", parse_mode="HTML")
        session.close()
        await session._ensure_started()
        pid = getattr(session.child, 'pid', 'N/A')
        text = f"🔄 <b>agy session (PID: {pid}) active</b>\n\nStart a new session or continue?"
        kb = InlineKeyboardMarkup(inline_keyboard=[
            [InlineKeyboardButton(text="🆕 New Session", callback_data="menu:reset")],
            [InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]
        ])
        await callback_query.message.edit_text(text, reply_markup=kb, parse_mode="HTML")
    elif action == "status":
        email = get_active_account_email(session.profile)
        await callback_query.answer(f"Status: OK | Account: {email} | Model: {session.model_name}", show_alert=True)

@router.message(Command("usage"))
async def cmd_usage(message: Message):
    if not is_allowed(message.from_user.id):
        return
    await message.bot.send_chat_action(chat_id=message.chat.id, action=ChatAction.TYPING)
    placeholder = await message.answer("📊 <i>Requesting full quota information...</i>", parse_mode="HTML")
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    formatted = await session.get_usage_info()
    await safe_edit_text(placeholder, formatted)

@router.message(Command("auth"))
@router.message(Command("account"))
async def cmd_account(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    email = get_active_account_email(session.profile)
    kb = InlineKeyboardMarkup(inline_keyboard=[
        [InlineKeyboardButton(text="🔄 Reconnect Authorization (Hot Reload)", callback_data="account:reload")],
        [InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")]
    ])
    text = (
        f"🔑 <b>Antigravity CLI Authorization</b>\n\n"
        f"👤 <b>Current account:</b> <code>{email}</code>\n"
        f"⚙️ <b>Auth Pickup:</b> Automatic (Hot Reload)\n\n"
        f"If you changed your account via <code>agy auth login</code> in the server terminal, "
        f"click the button below to force an update."
    )
    await message.answer(text, reply_markup=kb, parse_mode="HTML")

@router.callback_query(lambda c: c.data == "account:reload")
async def process_account_reload_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    session.close()
    email = get_active_account_email(session.profile)
    text = (
        f"⚡ <b>Authorization successfully reloaded!</b>\n\n"
        f"👤 <b>Active Account:</b> <code>{email}</code>\n\n"
        f"The next request will use the new credentials."
    )
    await callback_query.answer("Authorization reloaded!")
    await callback_query.message.edit_text(text, parse_mode="HTML")

@router.callback_query(lambda c: c.data == "mcp_health_check")
async def process_mcp_health_check_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    await callback_query.answer("Running MCP endpoint health checks...")
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    mgr = MCPManager(MCPConfigManager(profile=session.profile))
    results = await mgr.health_check_all()

    report_lines = [
        "🧪 <b>MCP Health Check Results</b>\n"
    ]
    for k, info in results.items():
        status_text = info.get("status", "unknown").lower()
        if info.get("ok"):
            status_icon = "✅"
        elif status_text == "disabled":
            status_icon = "⚪"
        elif "degraded" in status_text:
            status_icon = "⚠️"
        else:
            status_icon = "❌"
            
        name = info.get("name", k)
        status = status_text.upper()
        target = info.get("target", "N/A")
        report_lines.append(f"{status_icon} <b>{name}</b> ({status})\n   Target: <code>{target}</code>")

    report = "\n".join(report_lines)
    
    # --- Deep Ecosystem Health Injection ---
    try:
        import subprocess, json, os
        env = os.environ.copy()
        env["SEARXNG_URL"] = "http://127.0.0.1:8889"
        res = subprocess.check_output(
            ["/root/projects/TheNovaNodes/searxng-mcp-gateway/.venv/bin/python", "-c", 
             "import sys; sys.path.insert(0, '/root/projects/TheNovaNodes/searxng-mcp-gateway'); from searxng_gateway.server import ecosystem_health; import json; print(json.dumps(ecosystem_health()))"],
            timeout=15,
            env=env
        )
        eco_data = json.loads(res.decode())
        report += "\n\n🌐 <b>Search Ecosystem Status (Echelons)</b>\n"
        
        sx_status = eco_data.get('searxng', 'unknown')
        sx_icon = "🟢" if sx_status == "ok" else "🔴"
        report += f"{sx_icon} SearXNG (Local): {sx_status.upper()} ({eco_data.get('searxng_latency_ms', 0)}ms)\n"
        
        for prov, stat in eco_data.get("api_echelons", {}).items():
            report += f"├─ [{prov.upper()}]: {stat}\n"
    except Exception as e:
        report += f"\n\n🌐 <b>Search Ecosystem Status</b>\n⚠️ Could not fetch deeper metrics."
    # ---------------------------------------

    await callback_query.message.edit_text(report, reply_markup=get_mcp_keyboard(profile=session.profile), parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("toggle_mcp:"))
async def process_mcp_toggle_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    key_name = callback_query.data.split(":")[1]
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    mgr = MCPManager(MCPConfigManager(profile=session.profile))
    new_state = mgr.toggle_server(key_name)
    state_str = "enabled ✅" if new_state else "disabled ⚪"
    await callback_query.answer(f"MCP server {key_name} {state_str}")
    report = mgr.get_status_report()
    await callback_query.message.edit_text(report, reply_markup=get_mcp_keyboard(profile=session.profile), parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("set_model:"))
async def process_model_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    alias = callback_query.data.split(":")[1]
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    if session.set_model(alias):
        await callback_query.answer("Model changed!")
        await callback_query.message.edit_text(
            f"✅ <b>Model successfully changed!</b>\nNew model: <code>{session.model_name}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )
    else:
        await callback_query.answer("This model is already selected!")

@router.callback_query(lambda c: c.data and c.data.startswith("set_effort:"))
async def process_effort_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    level = callback_query.data.split(":")[1]
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    if session.set_effort(level):
        await callback_query.answer("Effort changed!")
        await callback_query.message.edit_text(
            f"✅ <b>Effort successfully changed!</b>\nReasoning level: <code>{session.effort.upper()}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )
    else:
        await callback_query.answer("This effort is already selected!")

@router.callback_query(lambda c: c.data and c.data.startswith("set_mode:"))
async def process_mode_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    mode_key = callback_query.data.split(":")[1]
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    if session.set_mode(mode_key):
        await callback_query.answer("Mode changed!")
        await callback_query.message.edit_text(
            f"✅ <b>Execution Mode successfully changed!</b>\nMode: <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )
    else:
        await callback_query.answer("This mode is already selected!")

@router.message(Command("reset"), Command("new"), Command("clear"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    new = session_manager.new_session(key)
    await message.answer(
        f"✨ <b>New session created!</b>\n\n"
        f"Settings saved: <code>{new.model_name}</code> / <code>{new.effort}</code> / <code>{AVAILABLE_MODES.get(new.mode, new.mode)}</code>\n"
        f"The next request will start a clean chat.",
        parse_mode="HTML"
    )


@router.message(Command("rename"))
async def cmd_rename(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    if not session.conversation_id:
        await message.answer("⚠️ No active session to rename. First start a chat or select one via /resume.", parse_mode="HTML")
        return
        
    parts = message.text.split(maxsplit=1)
    if len(parts) < 2:
        await message.answer("ℹ️ <b>How to use:</b>\nSend <code>/rename New Session Name</code>", parse_mode="HTML")
        return
        
    new_title = parts[1].strip()
    
    # Needs import: from src.conversations import rename_conversation
    from src.conversations import rename_conversation
    success = rename_conversation(session.conversation_id, new_title, profile=session.profile)
    
    if success:
        await message.answer(f"✅ <b>Session renamed!</b>\nNew name: <i>{new_title}</i>", parse_mode="HTML")
    else:
        await message.answer("❌ Error renaming. Database unavailable or ID not found.", parse_mode="HTML")

@router.message(Command("track_jules"))
async def cmd_track_jules(message: Message):
    if not is_allowed(message.from_user.id):
        return
        
    parts = message.text.split(maxsplit=1)
    if len(parts) < 2:
        await message.answer("ℹ️ <b>How to use:</b>\nSend <code>/track_jules JulesSessionName</code>", parse_mode="HTML")
        return
        
    session_name = parts[1].strip()
    from src.jules_monitor import ACTIVE_JULES_SESSIONS, ACTIVE_JULES_SESSIONS_LOCK
    async with ACTIVE_JULES_SESSIONS_LOCK:
        ACTIVE_JULES_SESSIONS[session_name] = message.chat.id
    
    await message.answer(f"✅ <b>Jules session added to monitoring!</b>\nName: <code>{session_name}</code>\n\nYou will receive a notification when it finishes.", parse_mode="HTML")


@router.callback_query(lambda c: c.data and c.data.startswith("jules_test:"))
async def process_jules_test_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    
    sess_hash = callback_query.data.split(":")[1]
    from src.jules_monitor import PENDING_JULES_PATCHES
    
    patch_data = PENDING_JULES_PATCHES.get(sess_hash)
    if not patch_data:
        await callback_query.answer("❌ Patch data expired or not found.", show_alert=True)
        return
        
    await callback_query.answer("🧪 Applying patch & running pytest...")
    status_msg = await callback_query.message.reply("⏳ <b>Applying Jules patch & running tests...</b>", parse_mode="HTML")
    
    import tempfile
    import os
    session_name = patch_data["session_name"]
    patch_text = patch_data["patch_text"]
    
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    target_dir = session.workspace if session.workspace else os.getcwd()
    
    try:
        with tempfile.NamedTemporaryFile("w", suffix=".patch", delete=False) as tmp:
            tmp.write(patch_text)
            tmp_path = tmp.name
            
        # 1. Apply patch using git apply
        apply_proc = await asyncio.create_subprocess_exec(
            "git", "apply", tmp_path,
            cwd=target_dir,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        apply_out, apply_err = await apply_proc.communicate()
        
        try:
            os.remove(tmp_path)
        except OSError:
            pass

        if apply_proc.returncode != 0:
            err_text = apply_err.decode('utf-8', errors='ignore') or apply_out.decode('utf-8', errors='ignore')
            await status_msg.edit_text(
                f"❌ <b>Failed to apply Jules patch!</b>\n"
                f"📌 <b>Session:</b> <code>{session_name}</code>\n\n"
                f"<pre><code>{err_text[:2000]}</code></pre>",
                parse_mode="HTML"
            )
            return

        # 2. Run pytest
        pytest_bin = os.path.join(target_dir, ".venv", "bin", "pytest")
        if not os.path.exists(pytest_bin):
            pytest_cmd = ["python3", "-m", "pytest"]
        else:
            pytest_cmd = [pytest_bin]
            
        test_proc = await asyncio.create_subprocess_exec(
            *pytest_cmd,
            cwd=target_dir,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE
        )
        test_out, test_err = await test_proc.communicate()
        
        output_str = test_out.decode('utf-8', errors='ignore') + "\n" + test_err.decode('utf-8', errors='ignore')
        output_str = output_str.strip()
        
        status_icon = "✅" if test_proc.returncode == 0 else "❌"
        verdict = "Pytest Passed Successfully!" if test_proc.returncode == 0 else "Pytest Failed!"
        
        result_text = (
            f"{status_icon} <b>Jules Patch Tested: {verdict}</b>\n"
            f"📌 <b>Session:</b> <code>{session_name}</code>\n"
            f"📁 <b>Directory:</b> <code>{target_dir}</code>\n\n"
            f"<b>Output:</b>\n<pre><code>{output_str[-3000:]}</code></pre>"
        )
        await safe_edit_text(status_msg, result_text)
        
    except Exception as e:
        logger.error(f"Error applying patch for {session_name}: {e}", exc_info=True)
        await status_msg.edit_text(f"❌ <b>Error running test pipeline:</b> {e}", parse_mode="HTML")


@router.callback_query(lambda c: c.data and c.data.startswith("sentinel:"))
async def handle_sentinel_callbacks(callback: CallbackQuery):
    if not is_allowed(callback.from_user.id):
        await callback.answer("Access denied", show_alert=True)
        return

    data = callback.data
    chat_id = callback.message.chat.id

    if data == "sentinel:menu":
        jobs = sentinel_scheduler.list_jobs()
        text = (
            "🛡️ <b>Autonomous Sentinel (Auto-Pilot Control Center)</b>\n\n"
            "Here you can set up pro-active background AI monitors that run autonomously without interrupting your active chat session.\n\n"
        )
        if jobs:
            text += f"📊 <b>Active Autonomous Jobs ({len(jobs)}):</b>\n"
            for j in jobs:
                text += f"• <code>{j['id']}</code> (Next: {j['next_run_time']})\n"
        else:
            text += "<i>No active scheduled jobs currently running.</i>"

        buttons = [
            [
                InlineKeyboardButton(text="➕ Add Preset: Morning Briefing (09:00)", callback_data="sentinel:preset:morning"),
            ],
            [
                InlineKeyboardButton(text="⚡ Add Preset: 6-Hour Health Check", callback_data="sentinel:preset:health6h"),
            ],
            [
                InlineKeyboardButton(text="🗑️ Manage / Delete Active Jobs", callback_data="sentinel:manage_delete"),
            ],
            [
                InlineKeyboardButton(text="◀️ Back to Main Menu", callback_data="menu:main")
            ]
        ]
        await callback.message.edit_text(text, reply_markup=InlineKeyboardMarkup(inline_keyboard=buttons), parse_mode="HTML")
        await callback.answer()

    elif data.startswith("sentinel:preset:"):
        preset_type = data.split(":")[-1]
        if preset_type == "morning":
            job_id = f"morning_brief_{chat_id}"
            cron_expr = "0 9 * * *"
            prompt = "Provide a high-level morning repo health check, git status, and key action items."
            title = "Morning Briefing (Daily at 09:00)"
        else:
            job_id = f"health_6h_{chat_id}"
            cron_expr = "0 */6 * * *"
            prompt = "Run routine health check on codebase, tests, and active tasks."
            title = "6-Hour Codebase Sentinel"

        try:
            sentinel_scheduler.add_sentinel_job(
                job_id=job_id,
                chat_id=chat_id,
                prompt=prompt,
                cron_expression=cron_expr
            )
            await callback.answer(f"✅ Created: {title}!", show_alert=True)
        except Exception as e:
            await callback.answer(f"❌ Error: {e}", show_alert=True)

        # Refresh menu
        await handle_sentinel_callbacks(callback)

    elif data == "sentinel:manage_delete":
        jobs = sentinel_scheduler.list_jobs()
        if not jobs:
            await callback.answer("No jobs to delete", show_alert=True)
            return

        buttons = []
        for j in jobs:
            buttons.append([
                InlineKeyboardButton(text=f"❌ Delete: {j['id']}", callback_data=f"sentinel:delete_job:{j['id']}")
            ])
        buttons.append([InlineKeyboardButton(text="◀️ Back to Sentinel Menu", callback_data="sentinel:menu")])
        
        await callback.message.edit_text(
            "🗑️ <b>Select a Sentinel Job to remove:</b>",
            reply_markup=InlineKeyboardMarkup(inline_keyboard=buttons),
            parse_mode="HTML"
        )
        await callback.answer()

    elif data.startswith("sentinel:delete_job:"):
        job_id = data.replace("sentinel:delete_job:", "")
        removed = sentinel_scheduler.remove_sentinel_job(job_id)
        if removed:
            await callback.answer(f"Deleted job {job_id}", show_alert=True)
        else:
            await callback.answer("Job not found", show_alert=True)

        callback.data = "sentinel:menu"
        await handle_sentinel_callbacks(callback)


@router.message(Command("debug"))
async def cmd_debug(message: Message):
    """Debug command: shows full session state for troubleshooting."""
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    pty_alive = bool(session.child and session.child.isalive())
    pty_pid = session.child.pid if session.child else None

    from src.conversations import get_latest_conversation_id
    latest_conv = get_latest_conversation_id(profile=session.profile)

    from pathlib import Path
    brain_base = session.profile.cli_state_dir / "brain" if session.profile else Path.home() / ".gemini" / "antigravity-cli" / "brain"
    brain_dirs_count = 0
    if brain_base.exists():
        brain_dirs_count = sum(1 for d in brain_base.iterdir() if d.is_dir() and not d.name.startswith("."))

    text = (
        f"🔍 <b>Debug Info — Session State</b>\n\n"
        f"<b>chat_id:</b> <code>{message.chat.id}</code>\n"
        f"<b>user_id:</b> <code>{message.from_user.id}</code>\n\n"
        f"<b>── Profile ──</b>\n"
        f"<b>profile_name:</b> <code>{session.profile.name if session.profile else 'default'}</code>\n"
        f"<b>state_dir:</b> <code>{session.profile.state_dir if session.profile else 'N/A'}</code>\n"
        f"<b>cli_state_dir:</b> <code>{session.profile.cli_state_dir if session.profile else 'N/A'}</code>\n\n"
        f"<b>── Session ──</b>\n"
        f"<b>conversation_id:</b> <code>{session.conversation_id or 'None (new session)'}</code>\n"
        f"<b>model:</b> <code>{session.model_name}</code>\n"
        f"<b>effort:</b> <code>{session.effort}</code>\n"
        f"<b>mode:</b> <code>{session.mode}</code>\n"
        f"<b>workspace:</b> <code>{session.workspace or 'Home Directory'}</code>\n\n"

        f"<b>── PTY Process ──</b>\n"
        f"<b>PTY alive:</b> <code>{pty_alive}</code>\n"
        f"<b>PTY PID:</b> <code>{pty_pid or 'N/A'}</code>\n"
        f"<b>auth_signature:</b> <code>{session.spawn_auth_signature[:24] + '...' if session.spawn_auth_signature else 'None'}</code>\n\n"
        f"<b>── System ──</b>\n"
        f"<b>active sessions:</b> <code>{len(session_manager.sessions)}</code>\n"
        f"<b>brain directories:</b> <code>{brain_dirs_count}</code>\n"
        f"<b>latest conv (profile):</b> <code>{latest_conv[:8] + '...' if latest_conv else 'None'}</code>\n"
    )
    await message.answer(text, parse_mode="HTML")

def get_workspace_keyboard() -> InlineKeyboardMarkup:
    """Build an interactive inline keyboard listing project folders in /root/lab and subdirectories."""
    import hashlib
    dirs_to_check = [
        Path("/root/lab"),
        Path("/root/lab/thedoctormes-hue"),
        Path("/root/lab/thenovanodes"),
        Path("/root/lab/playground"),
        Path("/root")
    ]
    buttons = []
    seen_paths = set()

    for base in dirs_to_check:
        if base.exists() and base.is_dir():
            for p in sorted(base.iterdir()):
                if p.is_dir() and not p.name.startswith("."):
                    p_str = str(p.resolve())
                    if p_str not in seen_paths and p_str != "/root":
                        seen_paths.add(p_str)
                        label = f"📁 {p.name}"
                        # Compact display path label
                        if len(label) > 30:
                            label = label[:27] + "..."
                        path_hash = hashlib.sha256(p_str.encode()).hexdigest()[:16]
                        buttons.append([InlineKeyboardButton(text=label, callback_data=f"set_ws:{path_hash}")])

    buttons.append([InlineKeyboardButton(text="🏠 Home directory (/root)", callback_data="set_ws:home")])
    buttons.append([InlineKeyboardButton(text="◀️ Back to Menu", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


@router.callback_query(lambda c: c.data and c.data.startswith("set_ws:"))
async def process_workspace_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    raw_hash = callback_query.data.split("set_ws:")[1]
    new_ws = None
    
    if raw_hash != "home":
        import hashlib
        dirs_to_check = [
            Path("/root/lab"),
            Path("/root/lab/thedoctormes-hue"),
            Path("/root/lab/thenovanodes"),
            Path("/root/lab/playground"),
            Path("/root")
        ]
        found = False
        for base in dirs_to_check:
            if base.exists() and base.is_dir():
                for p in base.iterdir():
                    if p.is_dir() and not p.name.startswith("."):
                        p_str = str(p.resolve())
                        if hashlib.sha256(p_str.encode()).hexdigest()[:16] == raw_hash:
                            new_ws = p_str
                            found = True
                            break
            if found:
                break
        
        if not found:
            await callback_query.answer("❌ Error: Folder not found", show_alert=True)
            return

    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)
    session.set_workspace(new_ws)

    display_ws = new_ws if new_ws else "Home Directory (/root)"
    await callback_query.answer(f"Workspace changed to {display_ws}")
    await callback_query.message.edit_text(
        f"✅ <b>Workspace successfully pinned!</b>\n\n"
        f"📂 <b>Current Directory:</b> <code>{display_ws}</code>\n\n"
        f"All subsequent commands will execute in this directory until you press <b>/reset</b>.",
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )


@router.message(Command("cd"))
async def cmd_cd(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    parts = message.text.split(maxsplit=1)
    
    if len(parts) < 2:
        await message.answer(
            f"📂 <b>Current Workspace:</b> <code>{session.workspace if session.workspace else 'Home Directory (/root)'}</code>\n\n"
            "Select a project folder below or send <code>/cd /path/to/folder</code>:",
            reply_markup=get_workspace_keyboard(),
            parse_mode="HTML"
        )
        return
        
    raw_path = parts[1].strip()
    if raw_path.lower() == "home":
        target_path = None
    else:
        p = Path(raw_path).expanduser().resolve()
        
        # Security check: must be under one of WORKSPACE_BASE_PATHS
        from src.config import config
        valid = False
        allowed_bases = config.get("WORKSPACE_BASE_PATHS", ["/root/lab", "/root/projects"])
        for base in allowed_bases:
            try:
                base_path = Path(base).expanduser().resolve()
                if p.is_relative_to(base_path):
                    valid = True
                    break
            except Exception as e:
                logger.debug(f"Failed to resolve workspace base {base}: {e}")
                
        if not valid:
            await message.answer(f"❌ <b>Access Denied!</b>\nPath <code>{raw_path}</code> is outside allowed workspace roots.", parse_mode="HTML")
            return
            
        if not p.exists() or not p.is_dir():
            await message.answer(f"❌ <b>Folder not found!</b>\nPath <code>{raw_path}</code> does not exist or is not a directory.", parse_mode="HTML")
            return
        target_path = str(p)
    
    session.set_workspace(target_path)
    display_ws = target_path if target_path else "Home Directory (/root)"
    await message.answer(
        f"✅ <b>Workspace pinned for session!</b>\n\n"
        f"📂 <b>New Workspace Directory:</b> <code>{display_ws}</code>",
        parse_mode="HTML"
    )


@router.message(Command("resume"))
async def cmd_resume(message: Message):
    if not is_allowed(message.from_user.id):
        return
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)
    kb = get_resume_keyboard(current_conversation_id=session.conversation_id, profile=session.profile)
    current_info = ""
    if session.conversation_id:
        if session.conversation_id == "latest":
            current_info = "\n\n💬 <b>Current:</b> Latest active (--continue)"
        else:
            current_info = f"\n\n💬 <b>Current:</b> <code>{session.conversation_id[:8]}...</code>"
    await message.answer(
        f"📂 <b>Select a saved session from agy CLI history to resume:</b>{current_info}",
        reply_markup=kb,
        parse_mode="HTML"
    )


@router.callback_query(lambda c: c.data and c.data.startswith("resume_set:"))
async def process_resume_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    conv_id = callback_query.data.split("resume_set:")[1]
    key = get_session_key(callback_query, callback_query.bot)
    session = session_manager.get_session(key)

    if conv_id == "new":
        key = get_session_key(callback_query, callback_query.bot)
        session_manager.new_session(key)
        text = f"✨ <b>New clean session created!</b>\nSettings saved. The next request will start a new chat."
    elif conv_id == "latest":
        session.set_conversation("latest")
        text = "🔄 <b>Resumed latest active agy CLI session (<code>--continue</code>)!</b>"
    else:
        session.set_conversation(conv_id)
        title = get_conversation_title(conv_id, profile=session.profile)
        title_display = f"\n📝 <b>Name:</b> <i>{title}</i>" if title else ""
        text = f"✅ <b>Session resumed!</b>\n\n🆔 <b>Conversation ID</b>: <code>{conv_id}</code>{title_display}\n\nThe next request will continue in the context of the selected chat."

    await callback_query.answer("Session switched!")
    await callback_query.message.edit_text(text, parse_mode="HTML")

from aiogram.exceptions import TelegramBadRequest, TelegramRetryAfter

def strip_html_tags(text: str) -> str:
    """Removes HTML tags and unescapes entities for plain text fallback."""
    import re
    clean = re.sub(r'<[^>]+>', '', text)
    return clean.replace('&lt;', '<').replace('&gt;', '>').replace('&amp;', '&')

async def safe_edit_text(target: Message, text: str):
    """Try sending with HTML parse_mode; if Telegram fails with syntax error, fall back to plain text. Handles Rate Limits."""
    try:
        await target.edit_text(text, parse_mode="HTML")
    except TelegramRetryAfter as e:
        logger.warning(f"Rate limited by Telegram. Waiting {e.retry_after} seconds...")
        await asyncio.sleep(e.retry_after)
        await safe_edit_text(target, text)
    except TelegramBadRequest as e:
        if "message is not modified" in str(e).lower():
            return
        logger.warning(f"HTML parse mode failed for edit_text, falling back to plain text: {e}")
        try:
            plain_text = strip_html_tags(text)
            await target.edit_text(plain_text, parse_mode=None)
        except TelegramRetryAfter as retry_e:
            await asyncio.sleep(retry_e.retry_after)
            await target.edit_text(plain_text, parse_mode=None)
        except TelegramBadRequest as inner_e:
            if "message is not modified" not in str(inner_e).lower():
                logger.warning(f"Fallback plain text also failed: {inner_e}")

async def safe_answer(target: Message, text: str):
    """Try sending with HTML parse_mode; if Telegram fails with syntax error, fall back to plain text. Handles Rate Limits."""
    try:
        await target.answer(text, parse_mode="HTML")
    except TelegramRetryAfter as e:
        logger.warning(f"Rate limited by Telegram. Waiting {e.retry_after} seconds...")
        await asyncio.sleep(e.retry_after)
        await safe_answer(target, text)
    except TelegramBadRequest as e:
        logger.warning(f"HTML parse mode failed for answer, falling back to plain text: {e}")
        try:
            plain_text = strip_html_tags(text)
            await target.answer(plain_text, parse_mode=None)
        except TelegramRetryAfter as retry_e:
            await asyncio.sleep(retry_e.retry_after)
            await target.answer(plain_text, parse_mode=None)
        except TelegramBadRequest as inner_e:
            logger.warning(f"Fallback plain text also failed: {inner_e}")

from aiogram.types import BufferedInputFile

async def send_response_chunks(message: Message, placeholder: Message, text: str, max_chunk_size: int = 3800):
    """Smart Hybrid response sender:
    - <= 3800 chars: Single message edit
    - 3801..8000 chars: Multi-chunk text delivery (up to 2 chunks)
    - > 8000 chars: First 2000 chars in chat + attached full response .md file
    """
    total_len = len(text)
    
    # Threshold for document attachment (8000 chars)
    if total_len > 8000:
        preview_text = text[:2000].rstrip() + "\n\n...\n\n📄 <i>[Response too large. Full version in the file below]</i>"
        await safe_edit_text(placeholder, preview_text)
        
        # Prepare .md file attachment
        file_bytes = b"\xef\xbb\xbf" + text.encode("utf-8")
        doc_file = BufferedInputFile(file_bytes, filename="agent_response.md")
        await message.answer_document(
            document=doc_file,
            caption=f"📄 <b>Full AntigravityTelegramAgent response</b> ({total_len} chars)",
            parse_mode="HTML"
        )
        return

    if total_len <= max_chunk_size:
        await safe_edit_text(placeholder, text)
        return

    # Multi-chunk splitting by paragraphs (\n\n or \n) preserving limits
    chunks = []
    current_chunk = []
    current_length = 0

    lines = text.split("\n")
    for line in lines:
        if current_length + len(line) + 1 > max_chunk_size:
            if current_chunk:
                chunks.append("\n".join(current_chunk))
                current_chunk = []
                current_length = 0
            
            # Handle exceptionally long single lines
            while len(line) > max_chunk_size:
                chunks.append(line[:max_chunk_size])
                line = line[max_chunk_size:]
            
            if line:
                current_chunk.append(line)
                current_length = len(line)
        else:
            current_chunk.append(line)
            current_length += len(line) + 1

    if current_chunk:
        chunks.append("\n".join(current_chunk))

    # Send first chunk as placeholder edit
    await safe_edit_text(placeholder, chunks[0])
    # Send remaining chunks as new messages
    for chunk in chunks[1:]:
        if chunk.strip():
            await safe_answer(message, chunk)

from pathlib import Path
from aiogram.types import FSInputFile
from src.conversations import get_latest_conversation_id

async def check_and_send_artifacts(message: Message, session):
    """Detect and send newly generated artifacts from agy session brain and artifacts directories to Telegram chat.

    Uses a multi-strategy approach to find the correct brain and artifacts directories for the profile:
    1. Check the session's profile cli_state_dir / brain and session's profile cli_state_dir / artifacts
    2. Check the session's tracked conversation_id directory (set by _detect_conversation_id)
    3. Fall back to latest conversation from agy CLI database for session's profile
    4. Scan ALL brain directories modified in the last 120 seconds

    Recursively walks directories, excluding system folders (.system_generated, scratch, .user_uploaded).
    Deduplicates artifacts by filename to prevent double-sending.
    """
    profile = getattr(session, "profile", None)
    if profile:
        profile_brain_base = profile.cli_state_dir / "brain"
        profile_artifacts_base = profile.cli_state_dir / "artifacts"
    else:
        profile_brain_base = Path.home() / ".gemini" / "antigravity-cli" / "brain"
        profile_artifacts_base = Path.home() / ".gemini" / "antigravity-cli" / "artifacts"

    now = time.time()
    ignore_dirs = {".system_generated", ".user_uploaded", "scratch"}
    scan_dirs = []

    if profile_artifacts_base.exists() and profile_artifacts_base.is_dir():
        scan_dirs.append(profile_artifacts_base)

    if profile_brain_base.exists() and profile_brain_base.is_dir():
        # Strategy 1: Use session's tracked conversation_id (populated by _detect_conversation_id)
        conv_id = session.conversation_id
        if conv_id and conv_id != "latest":
            target = profile_brain_base / conv_id
            if target.exists() and target.is_dir():
                scan_dirs.append(target)

        # Strategy 2: Fall back to latest conversation from agy CLI database
        if not any(d.parent == profile_brain_base for d in scan_dirs):
            fallback_id = get_latest_conversation_id(profile=profile)
            if fallback_id:
                target = profile_brain_base / fallback_id
                if target.exists() and target.is_dir():
                    scan_dirs.append(target)

        # Strategy 3: Scan any brain directory modified in the last 120 seconds
        try:
            for d in profile_brain_base.iterdir():
                if d.is_dir() and not d.name.startswith(".") and d not in scan_dirs:
                    try:
                        if now - d.stat().st_mtime < 120:
                            scan_dirs.append(d)
                    except OSError as e:
                        logger.debug(f"Could not stat directory {d}: {e}")
        except Exception as e:
            logger.warning(f"Failed to scan brain base directory: {e}")

    if not scan_dirs:
        logger.debug("No active artifact/brain scan directories found, skipping artifact check")
        return

    # Recursively find artifact files modified in the last 120 seconds
    artifacts_to_send = []
    canonical_state_dir = profile.cli_state_dir.resolve() if profile else (Path.home() / ".gemini" / "antigravity-cli").resolve()

    for brain_dir in scan_dirs:
        try:
            for item in brain_dir.rglob("*"):
                if item.is_symlink():
                    continue
                if not item.is_file():
                    continue
                try:
                    canonical_file = item.resolve()
                    if canonical_file.is_symlink():
                        continue
                    canonical_file.relative_to(canonical_state_dir)
                except (ValueError, RuntimeError, OSError) as escape_err:
                    logger.warning(f"Rejecting artifact path traversal / symlink escape candidate {item}: {escape_err}")
                    continue
                # Skip files inside system directories
                try:
                    rel_parts = item.relative_to(brain_dir).parts
                    if any(part in ignore_dirs for part in rel_parts):
                        continue
                except ValueError:
                    pass
                # Skip metadata and bot-generated response files
                if item.name.endswith(".metadata.json") or item.name == "agent_response.md":
                    continue
                try:
                    if now - item.stat().st_mtime < 120:
                        artifacts_to_send.append(item)
                except OSError as e:
                    logger.debug(f"Could not stat artifact {item}: {e}")
        except Exception as e:
            logger.warning(f"Failed to scan artifact directory {brain_dir}: {e}")

    # Deduplicate by filename (same artifact name from different strategies)
    seen_names = set()
    unique_artifacts = []
    for artifact in artifacts_to_send:
        if artifact.name not in seen_names:
            seen_names.add(artifact.name)
            unique_artifacts.append(artifact)

    if unique_artifacts:
        logger.info(f"📦 Found {len(unique_artifacts)} new artifact(s) to deliver for chat_id={message.chat.id}")

    for artifact in unique_artifacts:
        try:
            file_size_kb = artifact.stat().st_size / 1024
            
            with open(artifact, "rb") as f:
                content = f.read()
            if not content.startswith(b"\xef\xbb\xbf"):
                content = b"\xef\xbb\xbf" + content
                
            input_file = BufferedInputFile(content, filename=artifact.name)
            caption = f"📦 <b>Session Artifact</b>\n📄 <code>{artifact.name}</code> ({file_size_kb:.1f} KB)"
            await message.answer_document(document=input_file, caption=caption, parse_mode="HTML")
            logger.info(f"✅ Delivered artifact {artifact.name} ({file_size_kb:.1f} KB) to chat_id={message.chat.id}")
        except Exception as e:
            logger.error(f"❌ Failed to send artifact {artifact.name} to Telegram: {e}", exc_info=True)


from src.scheduler import sentinel_scheduler

@router.message(Command("sentinel_add"))
async def handle_sentinel_add(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    # Usage: /sentinel_add <job_id> "<cron_expression>" <prompt>
    # Example: /sentinel_add morning_brief "0 8 * * *" Daily morning health check and repo briefing
    args = message.text.split(maxsplit=3)
    if len(args) < 4:
        await message.answer(
            "🤖 <b>Autonomous Sentinel — Add Job</b>\n\n"
            "<b>Usage:</b>\n"
            "<code>/sentinel_add &lt;job_id&gt; \"&lt;cron_expression&gt;\" &lt;prompt&gt;</code>\n\n"
            "<b>Example:</b>\n"
            "<code>/sentinel_add morning_brief \"0 8 * * *\" Give me a morning repo health check.</code>",
            parse_mode="HTML"
        )
        return

    job_id = args[1]
    cron_expr = args[2].strip('"\'')
    prompt = args[3]

    try:
        sentinel_scheduler.add_sentinel_job(
            job_id=job_id,
            chat_id=message.chat.id,
            prompt=prompt,
            cron_expression=cron_expr
        )
        await message.answer(
            f"✅ <b>Sentinel Job Added!</b>\n\n"
            f"• <b>ID:</b> <code>{job_id}</code>\n"
            f"• <b>Cron:</b> <code>{cron_expr}</code>\n"
            f"• <b>Prompt:</b> <code>{prompt}</code>\n\n"
            f"<i>Execution will run in background Shadow PTY without affecting your current interactive chat.</i>",
            parse_mode="HTML"
        )
    except Exception as e:
        await message.answer(f"❌ <b>Failed to add Sentinel Job:</b> {e}", parse_mode="HTML")


@router.message(Command("sentinel_list"))
async def handle_sentinel_list(message: Message):
    if not is_allowed(message.from_user.id):
        return

    jobs = sentinel_scheduler.list_jobs()
    if not jobs:
        await message.answer("🤖 <b>Autonomous Sentinel:</b> No active scheduled jobs.", parse_mode="HTML")
        return

    text = "🤖 <b>Autonomous Sentinel Active Jobs:</b>\n\n"
    for j in jobs:
        text += (
            f"• <b>ID:</b> <code>{j['id']}</code>\n"
            f"  Next Run: <code>{j['next_run_time']}</code>\n"
            f"  Target Chat: <code>{j['args'][0]}</code>\n\n"
        )
    await message.answer(text, parse_mode="HTML")


@router.message(Command("sentinel_remove"))
async def handle_sentinel_remove(message: Message):
    if not is_allowed(message.from_user.id):
        return

    args = message.text.split(maxsplit=1)
    if len(args) < 2:
        await message.answer("Usage: <code>/sentinel_remove &lt;job_id&gt;</code>", parse_mode="HTML")
        return

    job_id = args[1].strip()
    removed = sentinel_scheduler.remove_sentinel_job(job_id)
    if removed:
        await message.answer(f"✅ Sentinel Job <code>{job_id}</code> removed.", parse_mode="HTML")
    else:
        await message.answer(f"⚠️ Sentinel Job <code>{job_id}</code> not found.", parse_mode="HTML")



@router.message()
async def handle_message(message: Message):
    if not is_allowed(message.from_user.id):
        return
    if not message.text:
        return

    # Trigger Telegram typing action
    await message.bot.send_chat_action(chat_id=message.chat.id, action=ChatAction.TYPING)
    placeholder = await message.answer("🤔 Thinking...")
    key = get_session_key(message, message.bot)
    session = session_manager.get_session(key)

    last_edit_time = 0
    last_text = ""
    response_text = ""

    try:
        import time
        async for partial_text in session.stream_response(message.text):
            response_text = partial_text
            now = time.time()
            if now - last_edit_time > 1.5 and partial_text.strip() and partial_text != last_text:
                last_edit_time = now
                last_text = partial_text
                disp_text = partial_text[:3800] + "\n\n<i>⏳ Typing...</i>" if len(partial_text) > 3800 else partial_text + "\n\n<i>⏳ Typing...</i>"
                await safe_edit_text(placeholder, disp_text)

        # Log structured execution audit log
        await log_audit_event(
            user_id=message.from_user.id,
            chat_id=message.chat.id,
            model_name=session.model_name,
            effort=session.effort,
            mode=session.mode,
            prompt=message.text,
            response_length=len(response_text)
        )

        if response_text.strip():
            await send_response_chunks(message, placeholder, response_text)
        else:
            await placeholder.edit_text(
                "⚠️ <b>Agent executed silently or returned no text.</b>\n\n"
                "📌 <i>Possible reasons:</i>\n"
                f"• The model <code>{session.model_name}</code> or <code>high</code> effort hid the reasoning phase or exceeded the PTY screen timeout.\n"
                "• There was a temporary pause on the model servers (Capacity/Thinking suppression).\n\n"
                "💡 <b>Solution:</b>\n"
                "1. Repeat the request or use <code>/models</code> to select a different model.\n"
                "2. Or lower the <code>/effort</code> to <code>medium</code>.",
                parse_mode="HTML"
            )

        # Check for newly generated artifact files and send them to the Telegram chat
        await check_and_send_artifacts(message, session)

    except Exception as e:
        logger.error(f"Error handling message for chat_id={message.chat.id}: {e}", exc_info=True)
        try:
            await safe_edit_text(placeholder, f"❌ <b>An error occurred:</b> {e}")
        except Exception as fallback_e:
            logger.error(f"Failed to deliver fallback error message to Telegram: {fallback_e}")

