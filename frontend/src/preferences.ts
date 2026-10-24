import { ref, watch } from 'vue'
const key = 'dbcompare.autoCompareAfterApply'
function readAutoCompare() {
  try { return localStorage.getItem(key) !== 'false' } catch { return true }
}
export const autoCompareAfterApply = ref(readAutoCompare())
watch(autoCompareAfterApply, value => {
  try { localStorage.setItem(key, String(value)) } catch { /* In-memory preference still works. */ }
})
