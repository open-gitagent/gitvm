FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive

# Base system
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    openssh-client \
    sudo \
    wget \
    iproute2 \
    && rm -rf /var/lib/apt/lists/*

# Node.js 20
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Python 3
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    && rm -rf /var/lib/apt/lists/*

# Create default user
RUN useradd -m -s /bin/bash -u 1000 user \
    && echo "user ALL=(ALL) NOPASSWD:ALL" >> /etc/sudoers

# Copy gitvm-agent binary (must be built before docker build)
COPY gitvm-agent /usr/local/bin/gitvm-agent
RUN chmod +x /usr/local/bin/gitvm-agent

# Create init script that starts the agent
RUN printf '#!/bin/sh\n\
/usr/local/bin/gitvm-agent &\n\
exec /bin/sh\n' > /sbin/gitvm-init && chmod +x /sbin/gitvm-init

# Default working directory
WORKDIR /home/user

CMD ["/sbin/gitvm-init"]
