import os
from typing import Any, List, Optional

from litellm.integrations.custom_guardrail import CustomGuardrail
from litellm.llms.custom_httpx.http_handler import (
    get_async_httpx_client,
    httpxSpecialProvider,
)
from litellm.types.guardrails import PiiEntityType


class NemoGuardrails(CustomGuardrail):
    def __init__(
        self,
        api_base: Optional[str] = None,
        config_id: Optional[str] = None,
        model: Optional[str] = None,
        **kwargs,
    ):
        self.api_base = (api_base or os.getenv("NEMO_GUARDRAILS_API_BASE", "http://nemo-guardrails:8000")).rstrip("/")
        self.config_id = config_id or os.getenv("NEMO_GUARDRAILS_CONFIG_ID", "litellm_tinyllama")
        self.model = model or os.getenv("NEMO_GUARDRAILS_MODEL", "tinyllama")
        super().__init__(**kwargs)

    async def apply_guardrail(
        self,
        inputs: dict[str, Any],
        request_data: dict,
        input_type: str,
        logging_obj: Optional[Any] = None,
    ) -> dict[str, Any]:
        async_client = get_async_httpx_client(llm_provider=httpxSpecialProvider.LoggingCallback)
        messages = self._messages_from_request(inputs=inputs, request_data=request_data)

        response = await async_client.post(
            f"{self.api_base}/v1/chat/completions",
            headers={"Content-Type": "application/json"},
            json={
                "model": self.model,
                "messages": messages,
                "guardrails": {"config_id": self.config_id},
                "temperature": 0,
                "max_tokens": 128,
            },
            timeout=30,
        )
        response.raise_for_status()

        content = response.json()["choices"][0]["message"].get("content") or ""
        if self._looks_blocked(content):
            raise Exception(content)

        return inputs

    def _messages_from_request(self, inputs: dict[str, Any], request_data: Optional[dict]) -> list[dict]:
        if request_data:
            messages = request_data.get("messages")
            if isinstance(messages, list) and messages:
                return messages

        texts = inputs.get("texts")
        if isinstance(texts, list) and texts:
            return [{"role": "user", "content": "\n".join(str(text) for text in texts)}]

        return [{"role": "user", "content": ""}]

    def _looks_blocked(self, content: str) -> bool:
        normalized = content.lower()
        blocked_markers = (
            "i can't respond",
            "i cannot respond",
            "can't help with that",
            "cannot help with that",
            "refuse to respond",
            "can't answer that",
            "cannot answer that",
        )
        return any(marker in normalized for marker in blocked_markers)
