from dataclasses import dataclass

@dataclass(frozen=True)
class SessionKey:
    """Dataclass representing a composite session identifier for multi-bot isolation."""
    bot_id: int
    chat_id: int

    def __str__(self) -> str:
        return f"{self.bot_id}:{self.chat_id}"

    def to_tuple(self) -> tuple[int, int]:
        return (self.bot_id, self.chat_id)
