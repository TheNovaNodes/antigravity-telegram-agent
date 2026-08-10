import asyncio
import logging
import time
from aiogram import Router, types
from aiogram.filters import Command
from aiogram.types import Message, InlineKeyboardMarkup, InlineKeyboardButton, CallbackQuery
from aiogram.enums import ChatAction

from src.config import ALLOWED_USER_IDS
from src.session_manager import session_manager
from src.cli_runner import AVAILABLE_MODELS, AVAILABLE_EFFORTS, AVAILABLE_MODES, get_active_account_email
from src.mcp_manager import mcp_manager
from src.audit import log_audit_event

logger = logging.getLogger(__name__)
router = Router()

def is_allowed(user_id: int) -> bool:
    return user_id in ALLOWED_USER_IDS

def get_main_menu_keyboard(session) -> InlineKeyboardMarkup:
    email = get_active_account_email()
    btn_email = email if len(email) <= 24 else email[:21] + "..."
    buttons = [
        [
            InlineKeyboardButton(text=f"🤖 {session.model_name.split('-')[0].upper()}", callback_data="menu:models"),
            InlineKeyboardButton(text=f"⚡ {session.effort.upper()}", callback_data="menu:effort")
        ],
        [
            InlineKeyboardButton(text=f"🎯 Mode: {AVAILABLE_MODES.get(session.mode, session.mode)}", callback_data="menu:mode")
        ],
        [
            InlineKeyboardButton(text=f"📂 {session.workspace if session.workspace else 'Home Dir'}", callback_data="menu:workspace")
        ],
        [
            InlineKeyboardButton(text=f"🔑 {btn_email}", callback_data="menu:account"),
            InlineKeyboardButton(text="📊 Квоты (/usage)", callback_data="menu:usage")
        ],
        [
            InlineKeyboardButton(text="🔌 MCP Серверы", callback_data="menu:mcp"),
            InlineKeyboardButton(text="🔄 Сбросить сессию", callback_data="menu:reset")
        ],
        [
            InlineKeyboardButton(text="⚡ Статус системы", callback_data="menu:status")
        ]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

from src.conversations import get_available_conversations, get_conversation_title
from src.jules_monitor import ACTIVE_JULES_SESSIONS

def get_menu_text(session, is_start=False) -> str:
    active_session_title = "Новая сессия (изолированная)"
    if session.conversation_id:
        if session.conversation_id == "latest":
            active_session_title = "Последняя активная сессия (--continue)"
        else:
            title = get_conversation_title(session.conversation_id)
            active_session_title = f"{title} ({session.conversation_id[:8]})" if title else f"ID: {session.conversation_id[:8]}"

    header = "👋 <b>Добро пожаловать в Antigravity Telegram Agent Control Center!</b>\n\nЯ — высокопроизводительный асинхронный мост к <b>Google Antigravity (agy)</b> с поддержкой MCP-инфраструктуры.\n\n" if is_start else "🎛️ <b>Antigravity Telegram Agent Control Center</b>\n\n"
    
    text = (
        f"{header}"
        f"💬 <b>Активная сессия:</b> <code>{active_session_title}</code>\n"
        f"🤖 <b>Модель:</b> <code>{session.model_name}</code>\n"
        f"⚡ <b>Reasoning Effort:</b> <code>{session.effort}</code>\n"
        f"🎯 <b>Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n"
        f"📂 <b>Workspace:</b> <code>{session.workspace if session.workspace else 'Home Directory'}</code>\n\n"
    )
    
    if is_start:
        text += (
            "<b>Доступные команды:</b>\n"
            "• /menu — Главное меню управления\n"
            "• /cd — Изменить рабочую папку (workspace)\n"
            "• /resume — Возобновить сохраненный диалог из истории\n"
            "• /mcp — Управление MCP серверами\n"
            "• /models — Выбор нейросетевой модели\n"
            "• /effort — Настройка глубины рассуждений\n"
            "• /mode — Режим работы (Standard/Plan)\n"
            "• /reset — Сброс сессии\n"
        )
    else:
        text += "Выбери параметр для настройки:"
        
    return text


def get_models_keyboard() -> InlineKeyboardMarkup:
    buttons = []
    for alias, full_name in AVAILABLE_MODELS.items():
        buttons.append([InlineKeyboardButton(text=f"🤖 {alias} ({full_name})", callback_data=f"set_model:{alias}")])
    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


def get_resume_keyboard() -> InlineKeyboardMarkup:
    conversations = get_available_conversations(limit=15)
    buttons = [
        [InlineKeyboardButton(text="🔄 Продолжить последнюю сессию (--continue)", callback_data="resume_set:latest")]
    ]
    for conv in conversations:
        date_part = f" ({conv['date']})" if conv['date'] else ""
        label = f"💬 {conv['summary'][:28]}{date_part}"
        buttons.append([InlineKeyboardButton(text=label, callback_data=f"resume_set:{conv['id']}")])

    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_effort_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        [
            InlineKeyboardButton(text="⚡ Low (Быстрый)", callback_data="set_effort:low"),
            InlineKeyboardButton(text="🧠 Medium (Баланс)", callback_data="set_effort:medium"),
            InlineKeyboardButton(text="🚀 High (Глубокий)", callback_data="set_effort:high")
        ],
        [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_mode_keyboard() -> InlineKeyboardMarkup:
    buttons = [
        [InlineKeyboardButton(text="💬 Standard Chat", callback_data="set_mode:default")],
        [InlineKeyboardButton(text="📋 Planning Mode (Планирование)", callback_data="set_mode:plan")],
        [InlineKeyboardButton(text="⚡ Auto-Edits Mode (Авто-правки)", callback_data="set_mode:accept-edits")],
        [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
    ]
    return InlineKeyboardMarkup(inline_keyboard=buttons)

def get_mcp_keyboard() -> InlineKeyboardMarkup:
    servers = mcp_manager.config_manager.config.get("servers", {})
    buttons = []
    
    icons = {
        "anythingllm": "🧠 AnythingLLM (Память)",
        "searxng": "🔍 SearXNG (Поиск)",
        "nextcloud": "💼 Nextcloud (CRM)",
        "anythingllm-control": "⚙️ AnythingLLM (Control)",
        "nextcloud-control": "⚙️ Nextcloud (Control)"
    }

    for key, srv in servers.items():
        state_icon = "✅" if srv.get("enabled") else "❌"
        btn_text = icons.get(key, f"🔌 {srv.get('name', key)}")
        # Shorten text if needed to fit nicely on mobile
        if len(btn_text) > 30:
            btn_text = btn_text[:27] + "..."
        buttons.append([InlineKeyboardButton(text=f"{btn_text}: {state_icon}", callback_data=f"toggle_mcp:{key}")])

    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)

@router.message(Command("start"), Command("help"))
async def cmd_start(message: Message):
    if not is_allowed(message.from_user.id):
        logger.warning(f"Unauthorized access attempt from user_id={message.from_user.id}")
        return

    session = session_manager.get_session(message.chat.id)
    await message.answer(
        get_menu_text(session, is_start=True),
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )

@router.message(Command("menu"))
async def cmd_menu(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        get_menu_text(session, is_start=False),
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )

@router.message(Command("mcp"))
async def cmd_mcp(message: Message):
    if not is_allowed(message.from_user.id):
        return
    report = mcp_manager.get_status_report()
    await message.answer(report, reply_markup=get_mcp_keyboard(), parse_mode="HTML")

@router.message(Command("models"))
async def cmd_models(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 <b>Текущая модель:</b> <code>{session.model_name}</code>\n\n"
        "Выбери модель для переключения:",
        reply_markup=get_models_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("effort"))
async def cmd_effort(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"⚡ <b>Текущий Reasoning Effort:</b> <code>{session.effort}</code>\n\n"
        "Выбери глубинное усилие рассуждения агента:",
        reply_markup=get_effort_keyboard(),
        parse_mode="HTML"
    )

@router.message(Command("mode"))
async def cmd_mode(message: Message):
    if not is_allowed(message.from_user.id):
        return
    session = session_manager.get_session(message.chat.id)
    await message.answer(
        f"🎯 <b>Текущий Execution Mode:</b> <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>\n\n"
        "Выбери режим исполнения:",
        reply_markup=get_mode_keyboard(),
        parse_mode="HTML"
    )

@router.callback_query(lambda c: c.data and c.data.startswith("menu:"))
async def process_menu_navigation(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    action = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)

    if action == "main":
        await callback_query.message.edit_text(
            get_menu_text(session, is_start=False),
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )
    elif action == "models":
        await callback_query.message.edit_text("🎯 <b>Выбор нейросетевой модели:</b>", reply_markup=get_models_keyboard(), parse_mode="HTML")
    elif action == "effort":
        await callback_query.message.edit_text("⚡ <b>Выбор глубинного уровня рассуждений (Effort):</b>", reply_markup=get_effort_keyboard(), parse_mode="HTML")
    elif action == "mode":
        await callback_query.message.edit_text("🎯 <b>Выбор режима выполнения (Execution Mode):</b>", reply_markup=get_mode_keyboard(), parse_mode="HTML")
    elif action == "workspace":
        await callback_query.message.edit_text(
            f"📂 <b>Текущий Workspace:</b> <code>{session.workspace if session.workspace else 'Home Directory (/root)'}</code>\n\n"
            "Выбери проект/папку для закрепления на всю сессию:",
            reply_markup=get_workspace_keyboard(),
            parse_mode="HTML"
        )
    elif action == "mcp":
        report = mcp_manager.get_status_report()
        await callback_query.message.edit_text(report, reply_markup=get_mcp_keyboard(), parse_mode="HTML")
    elif action == "reset":
        session_manager.reset_session(callback_query.message.chat.id)
        await callback_query.message.edit_text("🔄 <b>Сессия сброшена!</b> Следующий запрос начнет новый диалог.", parse_mode="HTML")
    elif action == "account":
        email = get_active_account_email()
        kb = InlineKeyboardMarkup(inline_keyboard=[
            [InlineKeyboardButton(text="🔄 Переподключить авторизацию (Hot Reload)", callback_data="account:reload")],
            [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
        ])
        text = (
            f"🔑 <b>Авторизация Antigravity CLI</b>\n\n"
            f"👤 <b>Текущий аккаунт:</b> <code>{email}</code>\n"
            f"⚙️ <b>Подхват авторизации:</b> Автоматический (Hot Reload)\n\n"
            f"Если вы сменили аккаунт через <code>agy auth login</code> в терминале сервера, "
            f"нажмите кнопку ниже для принудительного обновления."
        )
        await callback_query.message.edit_text(text, reply_markup=kb, parse_mode="HTML")
    elif action == "usage":
        await callback_query.bot.send_chat_action(chat_id=callback_query.message.chat.id, action=ChatAction.TYPING)
        await callback_query.answer("Запрашиваю полную информацию по квотам...")
        session = session_manager.get_session(callback_query.message.chat.id)
        formatted = await session.get_usage_info()
        kb = InlineKeyboardMarkup(inline_keyboard=[[InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]])
        try:
            await callback_query.message.edit_text(formatted, reply_markup=kb, parse_mode="HTML")
        except Exception:
            await safe_edit_text(callback_query.message, formatted)
    elif action == "status":
        email = get_active_account_email()
        await callback_query.answer(f"Status: OK | Account: {email} | Model: {session.model_name}", show_alert=True)

@router.message(Command("usage"))
async def cmd_usage(message: Message):
    if not is_allowed(message.from_user.id):
        return
    await message.bot.send_chat_action(chat_id=message.chat.id, action=ChatAction.TYPING)
    placeholder = await message.answer("📊 <i>Запрашиваю полную информацию по квотам моделей...</i>", parse_mode="HTML")
    session = session_manager.get_session(message.chat.id)
    formatted = await session.get_usage_info()
    await safe_edit_text(placeholder, formatted)

@router.message(Command("auth"))
@router.message(Command("account"))
async def cmd_account(message: Message):
    if not is_allowed(message.from_user.id):
        return
    email = get_active_account_email()
    kb = InlineKeyboardMarkup(inline_keyboard=[
        [InlineKeyboardButton(text="🔄 Переподключить авторизацию (Hot Reload)", callback_data="account:reload")],
        [InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")]
    ])
    text = (
        f"🔑 <b>Авторизация Antigravity CLI</b>\n\n"
        f"👤 <b>Текущий аккаунт:</b> <code>{email}</code>\n"
        f"⚙️ <b>Подхват авторизации:</b> Автоматический (Hot Reload)\n\n"
        f"Если вы сменили аккаунт через <code>agy auth login</code> в терминале сервера, "
        f"нажмите кнопку ниже для принудительного обновления."
    )
    await message.answer(text, reply_markup=kb, parse_mode="HTML")

@router.callback_query(lambda c: c.data == "account:reload")
async def process_account_reload_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    session = session_manager.get_session(callback_query.message.chat.id)
    session.close()
    email = get_active_account_email()
    text = (
        f"⚡ <b>Авторизация успешно перезагружена!</b>\n\n"
        f"👤 <b>Активный аккаунт:</b> <code>{email}</code>\n\n"
        f"Следующий запрос пойдет с новыми учетными данными."
    )
    await callback_query.answer("Авторизация перезагружена!")
    await callback_query.message.edit_text(text, parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("toggle_mcp:"))
async def process_mcp_toggle_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    key = callback_query.data.split(":")[1]
    new_state = mcp_manager.toggle_server(key)
    state_str = "включен ✅" if new_state else "отключен ⚪"
    await callback_query.answer(f"MCP сервер {key} {state_str}")
    report = mcp_manager.get_status_report()
    await callback_query.message.edit_text(report, reply_markup=get_mcp_keyboard(), parse_mode="HTML")

@router.callback_query(lambda c: c.data and c.data.startswith("set_model:"))
async def process_model_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    alias = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_model(alias):
        await callback_query.message.edit_text(
            f"✅ <b>Модель успешно изменена!</b>\nНовая модель: <code>{session.model_name}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )

@router.callback_query(lambda c: c.data and c.data.startswith("set_effort:"))
async def process_effort_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    level = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_effort(level):
        await callback_query.message.edit_text(
            f"✅ <b>Effort успешно изменен!</b>\nУровень рассуждений: <code>{session.effort.upper()}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )

@router.callback_query(lambda c: c.data and c.data.startswith("set_mode:"))
async def process_mode_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    mode_key = callback_query.data.split(":")[1]
    session = session_manager.get_session(callback_query.message.chat.id)
    if session.set_mode(mode_key):
        await callback_query.message.edit_text(
            f"✅ <b>Execution Mode успешно изменен!</b>\nРежим: <code>{AVAILABLE_MODES.get(session.mode, session.mode)}</code>",
            reply_markup=get_main_menu_keyboard(session),
            parse_mode="HTML"
        )

@router.message(Command("reset"))
async def cmd_reset(message: Message):
    if not is_allowed(message.from_user.id):
        return
    chat_id = message.chat.id
    if session_manager.reset_session(chat_id):
        await message.answer("🔄 <b>Сессия сброшена!</b>", parse_mode="HTML")
    else:
        await message.answer("ℹ️ Активной сессии не найдено.", parse_mode="HTML")


@router.message(Command("rename"))
async def cmd_rename(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    session = session_manager.get_session(message.chat.id)
    if not session.conversation_id:
        await message.answer("⚠️ Нет активной сессии для переименования. Сначала начните диалог или выберите через /resume.", parse_mode="HTML")
        return
        
    parts = message.text.split(maxsplit=1)
    if len(parts) < 2:
        await message.answer("ℹ️ <b>Как использовать:</b>\nОтправьте <code>/rename Новое Имя Сессии</code>", parse_mode="HTML")
        return
        
    new_title = parts[1].strip()
    
    # Needs import: from src.conversations import rename_conversation
    from src.conversations import rename_conversation
    success = rename_conversation(session.conversation_id, new_title)
    
    if success:
        await message.answer(f"✅ <b>Сессия переименована!</b>\nНовое имя: <i>{new_title}</i>", parse_mode="HTML")
    else:
        await message.answer("❌ Ошибка при переименовании. База данных недоступна или ID не найден.", parse_mode="HTML")

@router.message(Command("track_jules"))
async def cmd_track_jules(message: Message):
    if not is_allowed(message.from_user.id):
        return
        
    parts = message.text.split(maxsplit=1)
    if len(parts) < 2:
        await message.answer("ℹ️ <b>Как использовать:</b>\nОтправьте <code>/track_jules ИмяСессииJules</code>", parse_mode="HTML")
        return
        
    session_name = parts[1].strip()
    ACTIVE_JULES_SESSIONS[session_name] = message.chat.id
    
    await message.answer(f"✅ <b>Jules сессия добавлена в мониторинг!</b>\nИмя: <code>{session_name}</code>\n\nВы получите уведомление, когда она завершится.", parse_mode="HTML")


def get_workspace_keyboard() -> InlineKeyboardMarkup:
    """Build an interactive inline keyboard listing project folders in /root and /root/LabDoctorM."""
    import hashlib
    dirs_to_check = [Path("/root"), Path("/root/LabDoctorM/projects"), Path("/root/LabDoctorM/workspaces")]
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

    buttons.append([InlineKeyboardButton(text="🏠 Домашняя директория (/root)", callback_data="set_ws:home")])
    buttons.append([InlineKeyboardButton(text="◀️ Назад в меню", callback_data="menu:main")])
    return InlineKeyboardMarkup(inline_keyboard=buttons)


@router.callback_query(lambda c: c.data and c.data.startswith("set_ws:"))
async def process_workspace_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    raw_hash = callback_query.data.split("set_ws:")[1]
    new_ws = None
    
    if raw_hash != "home":
        import hashlib
        dirs_to_check = [Path("/root"), Path("/root/LabDoctorM/projects"), Path("/root/LabDoctorM/workspaces")]
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
            await callback_query.answer("❌ Ошибка: Папка не найдена", show_alert=True)
            return

    session = session_manager.get_session(callback_query.message.chat.id)
    session.set_workspace(new_ws)

    display_ws = new_ws if new_ws else "Home Directory (/root)"
    await callback_query.answer(f"Workspace изменен на {display_ws}")
    await callback_query.message.edit_text(
        f"✅ <b>Workspace успешно закреплен на всю сессию!</b>\n\n"
        f"📂 <b>Текущий проект/папка:</b> <code>{display_ws}</code>\n\n"
        f"Все последующие действия бота и CLI выполняются в этой папке до нажатия <b>/reset</b>.",
        reply_markup=get_main_menu_keyboard(session),
        parse_mode="HTML"
    )


@router.message(Command("cd"))
async def cmd_cd(message: Message):
    if not is_allowed(message.from_user.id):
        return
    
    session = session_manager.get_session(message.chat.id)
    parts = message.text.split(maxsplit=1)
    
    if len(parts) < 2:
        await message.answer(
            f"📂 <b>Текущий Workspace:</b> <code>{session.workspace if session.workspace else 'Home Directory (/root)'}</code>\n\n"
            "Выбери проект/папку из списка ниже или отправь <code>/cd /путь/к/папке</code>:",
            reply_markup=get_workspace_keyboard(),
            parse_mode="HTML"
        )
        return
        
    raw_path = parts[1].strip()
    if raw_path.lower() == "home":
        target_path = None
    else:
        p = Path(raw_path).expanduser().resolve()
        if not p.exists() or not p.is_dir():
            await message.answer(f"❌ <b>Папка не найдена!</b>\nПуть <code>{raw_path}</code> не существует или не является директорией.", parse_mode="HTML")
            return
        target_path = str(p)
    
    session.set_workspace(target_path)
    display_ws = target_path if target_path else "Home Directory (/root)"
    await message.answer(
        f"✅ <b>Workspace закреплен на всю сессию!</b>\n\n"
        f"📂 <b>Новая рабочая папка:</b> <code>{display_ws}</code>",
        parse_mode="HTML"
    )


@router.message(Command("resume"))
async def cmd_resume(message: Message):
    if not is_allowed(message.from_user.id):
        return
    kb = get_resume_keyboard()
    await message.answer(
        "📂 <b>Выберите сохраненную сессию из истории agy CLI для возобновления:</b>",
        reply_markup=kb,
        parse_mode="HTML"
    )


@router.callback_query(lambda c: c.data and c.data.startswith("resume_set:"))
async def process_resume_callback(callback_query: CallbackQuery):
    if not is_allowed(callback_query.from_user.id):
        return
    conv_id = callback_query.data.split("resume_set:")[1]
    session = session_manager.get_session(callback_query.message.chat.id)

    if conv_id == "latest":
        session.set_conversation("latest")
        text = "🔄 <b>Возобновлена последняя активная сессия agy CLI (<code>--continue</code>)!</b>"
    else:
        session.set_conversation(conv_id)
        text = f"✅ <b>Сессия возобновлена!</b>\n\n🆔 <b>Conversation ID</b>: <code>{conv_id}</code>\n\nСледующий запрос продолжится в контексте выложенного диалога."

    await callback_query.answer("Сессия переключена!")
    await callback_query.message.edit_text(text, parse_mode="HTML")

from aiogram.exceptions import TelegramBadRequest

async def safe_edit_text(target: Message, text: str):
    """Try sending with HTML parse_mode; if Telegram fails with syntax error, fall back to plain text."""
    try:
        await target.edit_text(text, parse_mode="HTML")
    except TelegramBadRequest as e:
        logger.warning(f"HTML parse mode failed for edit_text, falling back to plain text: {e}")
        await target.edit_text(text, parse_mode=None)

async def safe_answer(target: Message, text: str):
    """Try sending with HTML parse_mode; if Telegram fails with syntax error, fall back to plain text."""
    try:
        await target.answer(text, parse_mode="HTML")
    except TelegramBadRequest as e:
        logger.warning(f"HTML parse mode failed for answer, falling back to plain text: {e}")
        await target.answer(text, parse_mode=None)

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
        preview_text = text[:2000].rstrip() + "\n\n...\n\n📄 <i>[Ответ слишком большой. Полная версия в файле ниже]</i>"
        await safe_edit_text(placeholder, preview_text)
        
        # Prepare .md file attachment
        file_bytes = text.encode("utf-8")
        doc_file = BufferedInputFile(file_bytes, filename="agent_response.md")
        await message.answer_document(
            document=doc_file,
            caption=f"📄 <b>Полный ответ AntigravityTelegramAgent</b> ({total_len} символов)",
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
    """Detect and send newly generated artifacts from agy session brain directory to Telegram chat.

    Uses a multi-strategy approach to find the correct brain directory:
    1. Check the session's tracked conversation_id directory (set by _detect_conversation_id)
    2. Fall back to latest conversation from agy CLI database
    3. Scan ALL brain directories modified in the last 120 seconds

    Recursively walks directories, excluding system folders (.system_generated, scratch, .user_uploaded).
    Deduplicates artifacts by filename to prevent double-sending.
    """
    brain_base = Path.home() / ".gemini" / "antigravity-cli" / "brain"
    if not brain_base.exists():
        logger.debug(f"Brain base directory {brain_base} does not exist, skipping artifact check")
        return

    now = time.time()
    ignore_dirs = {".system_generated", ".user_uploaded", "scratch"}
    scan_dirs = []

    # Strategy 1: Use session's tracked conversation_id (populated by _detect_conversation_id)
    conv_id = session.conversation_id
    if conv_id and conv_id != "latest":
        target = brain_base / conv_id
        if target.exists() and target.is_dir():
            scan_dirs.append(target)

    # Strategy 2: Fall back to latest conversation from agy CLI database
    if not scan_dirs:
        fallback_id = get_latest_conversation_id()
        if fallback_id:
            target = brain_base / fallback_id
            if target.exists() and target.is_dir():
                scan_dirs.append(target)

    # Strategy 3: Scan any brain directory modified in the last 120 seconds
    try:
        for d in brain_base.iterdir():
            if d.is_dir() and not d.name.startswith(".") and d not in scan_dirs:
                try:
                    if now - d.stat().st_mtime < 120:
                        scan_dirs.append(d)
                except Exception:
                    pass
    except Exception as e:
        logger.warning(f"Failed to scan brain base directory: {e}")

    # Recursively find artifact files modified in the last 120 seconds
    artifacts_to_send = []
    for brain_dir in scan_dirs:
        try:
            for item in brain_dir.rglob("*"):
                if not item.is_file():
                    continue
                # Skip files inside system directories
                rel_parts = item.relative_to(brain_dir).parts
                if any(part in ignore_dirs for part in rel_parts):
                    continue
                # Skip metadata and bot-generated response files
                if item.name.endswith(".metadata.json") or item.name == "agent_response.md":
                    continue
                try:
                    if now - item.stat().st_mtime < 120:
                        artifacts_to_send.append(item)
                except Exception:
                    pass
        except Exception as e:
            logger.warning(f"Failed to scan brain directory {brain_dir}: {e}")

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
            input_file = FSInputFile(str(artifact), filename=artifact.name)
            caption = f"📦 <b>Артефакт сессии</b>\n📄 <code>{artifact.name}</code> ({file_size_kb:.1f} KB)"
            await message.answer_document(document=input_file, caption=caption, parse_mode="HTML")
            logger.info(f"✅ Delivered artifact {artifact.name} ({file_size_kb:.1f} KB) to chat_id={message.chat.id}")
        except Exception as e:
            logger.error(f"❌ Failed to send artifact {artifact.name} to Telegram: {e}", exc_info=True)


@router.message()
async def handle_message(message: Message):
    if not is_allowed(message.from_user.id):
        return
    if not message.text:
        return

    # Trigger Telegram typing action
    await message.bot.send_chat_action(chat_id=message.chat.id, action=ChatAction.TYPING)
    placeholder = await message.answer("🤔 Думаю...")
    session = session_manager.get_session(message.chat.id)

    async def update_thinking_status(msg: Message):
        import time
        start_time = time.time()
        states = ["🤔 Думаю.", "🤔 Думаю..", "🤔 Думаю..."]
        i = 0
        while True:
            await asyncio.sleep(2.5)
            elapsed = int(time.time() - start_time)
            i = (i + 1) % len(states)
            state = states[i]
            if elapsed > 45:
                text = f"{state}\n<i>Глубокий анализ ({elapsed} сек)</i>"
            elif elapsed > 15:
                text = f"{state}\n<i>Ожидание ответа модели ({elapsed} сек)</i>"
            else:
                text = f"{state} ({elapsed} сек)"
            try:
                await msg.edit_text(text, parse_mode="HTML")
            except Exception:
                pass

    status_task = asyncio.create_task(update_thinking_status(placeholder))

    try:
        response_text = await session.get_response(message.text)
        
        # Stop the live status updating
        status_task.cancel()
        
        # Log structured execution audit log
        log_audit_event(
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
                "⚠️ <b>Агент отработал молча или не вернул текст.</b>\n\n"
                "📌 <i>Возможные причины:</i>\n"
                f"• Модель <code>{session.model_name}</code> или <code>high</code> effort скрыла фазу мышления или превысила таймаут PTY-экрана.\n"
                "• На серверах модели возникла кратковременная пауза (Capacity/Thinking suppression).\n\n"
                "💡 <b>Решение:</b>\n"
                "1. Повторите запрос или используйте <code>/models</code> для выбора другой модели.\n"
                "2. Или снизите <code>/effort</code> до <code>medium</code>.",
                parse_mode="HTML"
            )

        # Check for newly generated artifact files and send them to the Telegram chat
        await check_and_send_artifacts(message, session)

    except Exception as e:
        status_task.cancel()
        logger.error(f"Error handling message for chat_id={message.chat.id}: {e}", exc_info=True)
        await safe_edit_text(placeholder, f"❌ <b>Произошла ошибка:</b> {e}")

