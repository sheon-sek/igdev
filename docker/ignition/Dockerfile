ARG IGNITION_VERSION=8.3.8
FROM inductiveautomation/ignition:${IGNITION_VERSION}

USER root
COPY .runtime/docker/modules/ /tmp/dev-modules/
RUN find /tmp/dev-modules -type f -name '*.modl' -exec cp -f '{}' /usr/local/bin/ignition/user-lib/modules/ \; \
    && rm -rf /tmp/dev-modules
