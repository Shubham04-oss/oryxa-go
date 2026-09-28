package secrets

import sharedsecrets "github.com/Shubham04-oss/oryxa-go/internal/secrets"

type KMSAPI = sharedsecrets.KMSAPI

// KMSDataKeys implements envelope data-key generation. KMS authenticates the
// encryption context, and decrypt must present exactly the same one.
type KMSDataKeys = sharedsecrets.KMSDataKeys
