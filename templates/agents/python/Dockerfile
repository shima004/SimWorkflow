# Copy this file to the root of your adf-sample-agent-python repository.
FROM python:3.13-slim

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1

# requirements.txt installs ADF Core from a Git repository.
RUN apt-get update \
    && apt-get install -y --no-install-recommends git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

ARG RCRS_CORE_REF=v0.2.3
COPY requirements.txt ./
# ADF Core declares this Git dependency through uv.sources. pip does not
# consume that setting, so install the corresponding package explicitly.
RUN pip install --no-cache-dir \
      "git+https://github.com/adf-python/rcrs-core-python.git@${RCRS_CORE_REF}" \
    && pip install --no-cache-dir -r requirements.txt

COPY . .

ENTRYPOINT ["python", "main.py"]
