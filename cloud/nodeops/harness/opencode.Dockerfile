# opencode harness layer, appended to Sandbox.base.Dockerfile by
# publish-nodeops-template.sh. Version kept in step with Sandbox.Dockerfile.

RUN npm install --global opencode-ai && \
    rm -rf /root/.npm && \
    opencode --version