FROM alpine:3.22.0 AS builder

# Set environment variables for minimal setup
ENV MINIMAL_SETUP=true
ENV USE_COMPLETE_NVIM_SETUP=false

# Install build dependencies
RUN apk update && apk add --no-cache \
    bash \
    git \
    curl \
    go \
    zsh

# Create user for building
RUN adduser -D -s /bin/bash builder
USER builder
WORKDIR /home/builder

# Install Go tools
RUN go install golang.org/x/tools/gopls@v0.20.0

# Build opencode from source
COPY --chown=builder:builder . /home/builder/opencode
WORKDIR /home/builder/opencode
RUN CGO_ENABLED=0 go build -o /home/builder/go/bin/opencode ./main.go

# Setup nvim config (minimal for configs only)
WORKDIR /home/builder
RUN mkdir -p /home/builder/.config
WORKDIR /home/builder/.config
RUN git clone --depth=1 https://github.com/obukhovaa/nvim-kickstart.git nvim

# Install Oh My Zsh and plugins
SHELL ["/bin/ash", "-o", "pipefail", "-c"]
WORKDIR /home/builder
RUN curl -fsSL https://raw.githubusercontent.com/ohmyzsh/ohmyzsh/master/tools/install.sh | KEEP_ZSHRC=yes RUNZSH=no CHSH=no sh && \
    git clone --depth=1 https://github.com/jeffreytse/zsh-vi-mode "${HOME}/.oh-my-zsh/custom/plugins/zsh-vi-mode" && \
    git clone --depth=1 https://github.com/zsh-users/zsh-syntax-highlighting.git "${HOME}/.oh-my-zsh/custom/plugins/zsh-syntax-highlighting" && \
    git clone --depth=1 https://github.com/zsh-users/zsh-autosuggestions "${HOME}/.oh-my-zsh/custom/plugins/zsh-autosuggestions"


# Final runtime stage
FROM alpine:3.22.0

# Install runtime dependencies
RUN apk update && apk add --no-cache \
    bash \
    git \
    curl \
    wget \
    tar \
    ripgrep \
    fzf \
    tmux \
    vim \
    zsh \
    perl \
    bind-tools \
    shadow \
    direnv \
    nodejs \
    npm \
    ncurses

# Create agent user
RUN adduser -D -s /bin/zsh agent
USER root

# Copy built binaries and configs from builder stage
COPY --from=builder /home/builder/go/bin/opencode /usr/local/bin/opencode
COPY --from=builder /home/builder/go/bin/gopls /usr/local/bin/gopls
COPY --from=builder /home/builder/.oh-my-zsh /home/agent/.oh-my-zsh
COPY --from=builder /home/builder/.config/nvim /home/agent/.config/nvim

# Install language servers in runtime stage to ensure dependencies are available
RUN npm install -g bash-language-server@5.1.2 typescript-language-server@4.3.3 typescript@5.3.3

# Copy configuration files from nvim setup
RUN cp /home/agent/.config/nvim/tmux/.tmux.conf /home/agent/.tmux.conf && \
    cp /home/agent/.config/nvim/tmux/.tmux.conf.local /home/agent/.tmux.conf.local && \
    cp /home/agent/.config/nvim/zsh/.zshrc /home/agent/.zshrc && \
    cp /home/agent/.config/nvim/vim/.vimrc /home/agent/.vimrc

# Copy application files
COPY scripts/agent.sh /home/agent/agent.sh

# Create workspace directory and set permissions
RUN mkdir -p /workspace && \
    chmod +x /home/agent/agent.sh && \
    chown -R agent:agent /home/agent && \
    chown -R agent:agent /workspace

USER agent
WORKDIR /workspace

ENV EDITOR=vim
ENV TERM=xterm-256color
ENV COLORTERM=truecolor

# Use agent.sh as entrypoint
ENTRYPOINT ["/home/agent/agent.sh"]
