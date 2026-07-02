FROM python:3.12-slim

WORKDIR /app

COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt

COPY staging_common.py gateway.py ./

ENV BROWSER_GATEWAY_HOST=0.0.0.0
ENV BROWSER_GATEWAY_PORT=8443
ENV BROWSER_CDP_UPSTREAM=http://host.docker.internal:9222
ENV BROWSER_STAGING_DIR=/staging

EXPOSE 8443

CMD ["python", "gateway.py"]
