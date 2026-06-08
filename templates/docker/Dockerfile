FROM ubuntu:22.04

# Avoid prompts from apt
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    curl \
    git \
    build-essential \
    python3 \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install OpenCode AI globally
RUN npm install -g opencode-ai

# Expose OpenCode server port
EXPOSE 36000

# Entrypoint runs the OpenCode server
ENTRYPOINT ["opencode", "serve", "--port", "36000", "--hostname", "0.0.0.0"]
