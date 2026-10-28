# Build with an isolated context containing rcrs-server/ extracted from the
# original image and java-wrapper.sh copied from this directory.
ARG LAUNCHER_IMAGE=ghcr.io/shima004/rcrs-server@sha256:bcf1b509bdf5eddd3549b0b7c0199d0f116e3d6193ef1513eeaae53af6ce9fe8
FROM ${LAUNCHER_IMAGE} AS launcher
FROM ${LAUNCHER_IMAGE}

USER root
RUN rm -rf /opt/rcrs-server && mkdir -p /opt/rcrs-server /opt/simworkflow/bin
COPY --chown=rcrs:rcrs rcrs-server/ /opt/rcrs-server/
COPY --from=launcher --chown=rcrs:rcrs /opt/rcrs-server/jars/rcrs-server-launcher.jar /opt/rcrs-server/jars/rcrs-server-launcher.jar
COPY --chmod=0755 java-wrapper.sh /opt/simworkflow/bin/java
COPY source.json /usr/share/simworkflow/legacy-source.json
RUN mkdir -p /opt/rcrs-server/logs && chown rcrs:rcrs /opt/rcrs-server/logs

ENV PATH="/opt/simworkflow/bin:${PATH}" \
    RCRS_HOME=/opt/rcrs-server
LABEL org.opencontainers.image.title="Legacy RCRS server for SimWorkflow" \
      org.opencontainers.image.description="Original server sources and JARs with a compatible launcher and score-log routing"
USER rcrs
WORKDIR /opt/rcrs-server
ENTRYPOINT ["java", "-jar", "/opt/rcrs-server/jars/rcrs-server-launcher.jar"]
CMD ["comprun", "--nogui"]
