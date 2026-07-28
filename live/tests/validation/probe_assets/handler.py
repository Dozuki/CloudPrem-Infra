import os
import ssl

import pymysql


def handler(event, context):
    try:
        # VERIFY_CA semantics: the chain is validated against the baked-in RDS CA
        # bundle (MITM defense), but hostname checking stays off - RDS certificates
        # carry the INSTANCE endpoint SAN while this connects via the CLUSTER
        # endpoint, so check_hostname would reject legitimate certs.
        sctx = ssl.create_default_context(
            cafile=os.path.join(os.path.dirname(__file__), "rds-ca.pem"))
        sctx.check_hostname = False
        sctx.verify_mode = ssl.CERT_REQUIRED
        conn = pymysql.connect(host=event["host"], user=event["user"],
                               password=event["password"], database="harness_dr",
                               connect_timeout=10, ssl=sctx)
        with conn.cursor() as cur:
            cur.execute("SELECT COUNT(*), MAX(wrote_at) FROM heartbeat WHERE run_id=%s",
                        (event["run_id"],))
            n, mx = cur.fetchone()
        conn.close()
        return {"count": int(n), "max_wrote_at": str(mx) if mx else ""}
    except Exception as e:
        return {"count": -1, "max_wrote_at": "", "error": f"{type(e).__name__}: {e}"}
