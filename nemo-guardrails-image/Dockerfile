FROM python:3.12-slim

RUN apt-get update && apt-get install -y --no-install-recommends gcc g++ \
  && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/nemo-guardrails
COPY vendor/nemo-guardrails/ /opt/nemo-guardrails/

# Install only the server extras (FastAPI/uvicorn/openai types).
RUN pip install --no-cache-dir ".[server]"

EXPOSE 8000

ENTRYPOINT ["nemoguardrails"]
CMD ["server", "--verbose", "--config=/config"]

