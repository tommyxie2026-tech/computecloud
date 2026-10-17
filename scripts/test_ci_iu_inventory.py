import unittest
from iu_host_inventory import preflight

class HostInventoryTest(unittest.TestCase):
    def fixture(self):
        return {"schema_version":"iu-independent-host-inventory-v1","computecloud_commit":"a"*40,
                "hosts":[{"worker_id":"w"+str(i),"host_identity_sha256":str(i)*64,"worker_binary_sha256":"a"*64,
                          "runtime_profile":"fixture","runtime_version":"1","model_configuration_ref":"fixture-only"} for i in (1,2)]}
    def test_same_runner_with_multiple_pids_is_not_independent(self):
        value=self.fixture();value["hosts"][1]["host_identity_sha256"]=value["hosts"][0]["host_identity_sha256"]
        value["hosts"][0]["pid"]=1;value["hosts"][1]["pid"]=2
        self.assertFalse(preflight(value)["identity_fields_complete"])
    def test_complete_inventory_does_not_claim_actual_acceptance(self):
        result=preflight(self.fixture())
        self.assertTrue(result["identity_fields_complete"])
        self.assertEqual(result["acceptance_status"],"NOT_RUN")
        self.assertEqual(result["real_provider_acceptance"],"NOT_RUN")
    def test_missing_host_is_not_ready(self):
        value=self.fixture();value["hosts"]=value["hosts"][:1]
        self.assertFalse(preflight(value)["identity_fields_complete"])
