# codex harness layer, appended to Sandbox.base.Dockerfile by
# publish-nodeops-template.sh. Version kept in step with Sandbox.Dockerfile.

RUN npm install --global @openai/codex@0.147.0 && \
    rm -rf /root/.npm && \
    codex --version
