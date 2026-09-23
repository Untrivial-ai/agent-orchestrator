# claude-code harness layer, appended to Sandbox.base.Dockerfile by
# publish-nodeops-template.sh. Version kept in step with Sandbox.Dockerfile.

RUN npm install --global @anthropic-ai/claude-code@2.1.228 && \
    ln -sfn "$(npm root --global)/@anthropic-ai/claude-code/cli-wrapper.cjs" \
        /usr/local/bin/claude && \
    rm -rf /root/.npm && \
    claude --version
