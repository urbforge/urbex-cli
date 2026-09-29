# Urbex runtime image for `runtime: python` - see
# docs/decisions/0018-image-build-komodo-gitea-registry.md in urbforge/urbex.
# Installs requirements.txt if present and runs main.py, which must
# listen on $PORT.
FROM docker.io/library/python:3.12-slim
ENV PYTHONUNBUFFERED=1
RUN export DEBIAN_FRONTEND=noninteractive && apt-get update && apt-get install -y --no-install-recommends wget \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY . .
RUN if [ -f requirements.txt ]; then pip install --no-cache-dir -r requirements.txt; fi
RUN useradd --no-create-home app
USER app
CMD ["python", "main.py"]
